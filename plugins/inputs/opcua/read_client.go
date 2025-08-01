package opcua

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/gopcua/opcua/ua"

	"github.com/influxdata/telegraf"
	"github.com/influxdata/telegraf/config"
	"github.com/influxdata/telegraf/plugins/common/opcua"
	"github.com/influxdata/telegraf/plugins/common/opcua/input"
	"github.com/influxdata/telegraf/selfstat"
)

type readClientWorkarounds struct {
	UseUnregisteredReads bool `toml:"use_unregistered_reads"`
}

type readClientConfig struct {
	ReconnectErrorThreshold *uint64               `toml:"reconnect_error_threshold"`
	ReadRetryTimeout        config.Duration       `toml:"read_retry_timeout"`
	ReadRetries             uint64                `toml:"read_retry_count"`
	ReadClientWorkarounds   readClientWorkarounds `toml:"request_workarounds"`
	input.InputClientConfig
}

// readClient Requests the current values from the required nodes when gather is called.
type readClient struct {
	*input.OpcUAInputClient

	ReconnectErrorThreshold uint64
	ReadRetryTimeout        time.Duration
	ReadRetries             uint64
	ReadSuccess             selfstat.Stat
	ReadError               selfstat.Stat
	Workarounds             readClientWorkarounds

	// internal values
	reqIDs []*ua.ReadValueID
	ctx    context.Context

	// Track last session error to force reconnection
	forceReconnect bool
}

func (rc *readClientConfig) createReadClient(log telegraf.Logger) (*readClient, error) {
	inputClient, err := rc.InputClientConfig.CreateInputClient(log)
	if err != nil {
		return nil, err
	}

	tags := map[string]string{
		"endpoint": inputClient.Config.OpcUAClientConfig.Endpoint,
	}

	if rc.ReadRetryTimeout == 0 {
		rc.ReadRetryTimeout = config.Duration(100 * time.Millisecond)
	}

	// Set default for ReconnectErrorThreshold if not configured
	// Use the default value of reconnect after every error and
	// allow the user to override that setting including forcing
	// a reconnect after every cycle by setting zero.
	reconnectThreshold := uint64(1)
	if rc.ReconnectErrorThreshold != nil {
		reconnectThreshold = *rc.ReconnectErrorThreshold
	}

	return &readClient{
		OpcUAInputClient:        inputClient,
		ReconnectErrorThreshold: reconnectThreshold,
		ReadRetryTimeout:        time.Duration(rc.ReadRetryTimeout),
		ReadRetries:             rc.ReadRetries,
		ReadSuccess:             selfstat.Register("opcua", "read_success", tags),
		ReadError:               selfstat.Register("opcua", "read_error", tags),
		Workarounds:             rc.ReadClientWorkarounds,
	}, nil
}

func (o *readClient) connect() error {
	o.ctx = context.Background()
	o.forceReconnect = false

	if err := o.OpcUAClient.Connect(o.ctx); err != nil {
		return fmt.Errorf("connect failed: %w", err)
	}

	// Make sure we setup the node-ids correctly after reconnect
	// as the server might be restarted and IDs changed
	if err := o.OpcUAInputClient.InitNodeIDs(); err != nil {
		return fmt.Errorf("initializing node IDs failed: %w", err)
	}

	o.reqIDs = make([]*ua.ReadValueID, 0, len(o.NodeIDs))
	if o.Workarounds.UseUnregisteredReads {
		for _, nid := range o.NodeIDs {
			o.reqIDs = append(o.reqIDs, &ua.ReadValueID{NodeID: nid})
		}
	} else {
		regResp, err := o.Client.RegisterNodes(o.ctx, &ua.RegisterNodesRequest{
			NodesToRegister: o.NodeIDs,
		})
		if err != nil {
			return fmt.Errorf("registering nodes failed: %w", err)
		}

		for _, v := range regResp.RegisteredNodeIDs {
			o.reqIDs = append(o.reqIDs, &ua.ReadValueID{NodeID: v})
		}
	}

	if err := o.read(); err != nil {
		return fmt.Errorf("get data failed: %w", err)
	}

	return nil
}

func (o *readClient) ensureConnected() error {
	// Force reconnection if we had a session error in the previous cycle
	if o.forceReconnect || o.State() == opcua.Disconnected || o.State() == opcua.Closed {
		// If we're forcing a reconnection, but we're not in Disconnected state,
		// explicitly disconnect first
		if o.State() != opcua.Disconnected && o.State() != opcua.Closed {
			if err := o.Disconnect(context.Background()); err != nil {
				o.Log.Debug("Error while disconnecting: ", err)
			}
		}
		return o.connect()
	}
	return nil
}

func (o *readClient) currentValues() ([]telegraf.Metric, error) {
	if err := o.ensureConnected(); err != nil {
		return nil, err
	}

	if state := o.State(); state != opcua.Connected {
		return nil, fmt.Errorf("not connected, in state %q", state)
	}

	if err := o.read(); err != nil {
		// We do not return the disconnect error, as this would mask the
		// original problem, but we do log it
		if derr := o.Disconnect(context.Background()); derr != nil {
			o.Log.Debug("Error while disconnecting: ", derr)
		}

		return nil, err
	}

	metrics := make([]telegraf.Metric, 0, len(o.NodeMetricMapping))
	// Parse the resulting data into metrics
	for i := range o.NodeIDs {
		if !o.StatusCodeOK(o.LastReceivedData[i].Quality) {
			continue
		}

		metrics = append(metrics, o.MetricForNode(i))
	}

	return metrics, nil
}

func (o *readClient) read() error {
	req := &ua.ReadRequest{
		MaxAge:             2000,
		TimestampsToReturn: ua.TimestampsToReturnBoth,
		NodesToRead:        o.reqIDs,
	}

	var count uint64

	for {
		count++

		// Try to update the values for all registered nodes
		resp, err := o.Client.Read(o.ctx, req)
		if err == nil {
			// Success, update the node values and exit
			o.ReadSuccess.Incr(1)
			o.forceReconnect = false
			for i, d := range resp.Results {
				o.UpdateNodeValue(i, d)
			}
			return nil
		}

		o.ReadError.Incr(1)

		isSessionError := errors.Is(err, ua.StatusBadSessionIDInvalid) ||
			errors.Is(err, ua.StatusBadSessionNotActivated) ||
			errors.Is(err, ua.StatusBadSecureChannelIDInvalid)

		// Flag session error for next cycle if encountered
		if isSessionError {
			o.forceReconnect = true
		}

		switch {
		case count > o.ReadRetries:
			// We exceeded the number of retries and should exit
			return fmt.Errorf("reading %s nodes failed after %d attempts: %w",
				nodeTypeLabel(o.Workarounds.UseUnregisteredReads), count, err)
		case isSessionError:
			// Retry after the defined period as session and channels should be refreshed
			o.Log.Debugf("reading failed with %v, retry %d / %d...", err, count, o.ReadRetries)
			time.Sleep(o.ReadRetryTimeout)
		default:
			// Non-retryable error, there is nothing we can do
			return fmt.Errorf("reading %s nodes failed: %w",
				nodeTypeLabel(o.Workarounds.UseUnregisteredReads), err)
		}
	}
}

// Helper function to provide more accurate error messages
func nodeTypeLabel(useUnregistered bool) string {
	if useUnregistered {
		return "unregistered"
	}
	return "registered"
}
