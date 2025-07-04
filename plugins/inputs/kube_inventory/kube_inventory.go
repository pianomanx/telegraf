//go:generate ../../../tools/readme_config_includer/generator
package kube_inventory

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/influxdata/telegraf"
	"github.com/influxdata/telegraf/config"
	"github.com/influxdata/telegraf/filter"
	"github.com/influxdata/telegraf/plugins/common/tls"
	"github.com/influxdata/telegraf/plugins/inputs"
)

//go:embed sample.conf
var sampleConfig string

var availableCollectors = map[string]func(ctx context.Context, acc telegraf.Accumulator, ki *KubernetesInventory){
	"daemonsets":             collectDaemonSets,
	"deployments":            collectDeployments,
	"endpoints":              collectEndpoints,
	"ingress":                collectIngress,
	"nodes":                  collectNodes,
	"pods":                   collectPods,
	"services":               collectServices,
	"statefulsets":           collectStatefulSets,
	"persistentvolumes":      collectPersistentVolumes,
	"persistentvolumeclaims": collectPersistentVolumeClaims,
	"resourcequotas":         collectResourceQuotas,
	"secrets":                collectSecrets,
}

const (
	daemonSetMeasurement             = "kubernetes_daemonset"
	deploymentMeasurement            = "kubernetes_deployment"
	endpointMeasurement              = "kubernetes_endpoint"
	ingressMeasurement               = "kubernetes_ingress"
	nodeMeasurement                  = "kubernetes_node"
	persistentVolumeMeasurement      = "kubernetes_persistentvolume"
	persistentVolumeClaimMeasurement = "kubernetes_persistentvolumeclaim"
	podContainerMeasurement          = "kubernetes_pod_container"
	serviceMeasurement               = "kubernetes_service"
	statefulSetMeasurement           = "kubernetes_statefulset"
	resourcequotaMeasurement         = "kubernetes_resourcequota"
	certificateMeasurement           = "kubernetes_certificate"

	defaultServiceAccountPath = "/var/run/secrets/kubernetes.io/serviceaccount/token"
)

type KubernetesInventory struct {
	URL             string          `toml:"url"`
	KubeletURL      string          `toml:"url_kubelet"`
	BearerToken     string          `toml:"bearer_token"`
	Namespace       string          `toml:"namespace"`
	ResponseTimeout config.Duration `toml:"response_timeout"` // Timeout specified as a string - 3s, 1m, 1h
	ResourceExclude []string        `toml:"resource_exclude"`
	ResourceInclude []string        `toml:"resource_include"`
	MaxConfigMapAge config.Duration `toml:"max_config_map_age"`

	SelectorInclude []string `toml:"selector_include"`
	SelectorExclude []string `toml:"selector_exclude"`

	NodeName string          `toml:"node_name"`
	Log      telegraf.Logger `toml:"-"`

	tls.ClientConfig
	client     *client
	httpClient *http.Client

	selectorFilter filter.Filter
}

func (*KubernetesInventory) SampleConfig() string {
	return sampleConfig
}

func (ki *KubernetesInventory) Init() error {
	// If bearer_token is not provided, use the default service account.
	if ki.BearerToken == "" {
		ki.BearerToken = defaultServiceAccountPath
	}

	var err error
	ki.client, err = newClient(ki.URL, ki.Namespace, ki.BearerToken, time.Duration(ki.ResponseTimeout), ki.ClientConfig)

	if err != nil {
		return err
	}
	if ki.ResponseTimeout < config.Duration(time.Second) {
		ki.ResponseTimeout = config.Duration(time.Second * 5)
	}
	// Only create an http client if we have a kubelet url
	if ki.KubeletURL != "" {
		ki.httpClient, err = newHTTPClient(ki.ClientConfig, ki.BearerToken, ki.ResponseTimeout)

		if err != nil {
			ki.Log.Warnf("unable to create http client: %v", err)
		}
	}
	return nil
}

func (ki *KubernetesInventory) Gather(acc telegraf.Accumulator) (err error) {
	resourceFilter, err := filter.NewIncludeExcludeFilter(ki.ResourceInclude, ki.ResourceExclude)
	if err != nil {
		return err
	}

	ki.selectorFilter, err = filter.NewIncludeExcludeFilter(ki.SelectorInclude, ki.SelectorExclude)
	if err != nil {
		return err
	}

	wg := sync.WaitGroup{}
	ctx := context.Background()

	for collector, f := range availableCollectors {
		if resourceFilter.Match(collector) {
			wg.Add(1)
			go func(f func(ctx context.Context, acc telegraf.Accumulator, k *KubernetesInventory)) {
				defer wg.Done()
				f(ctx, acc, ki)
			}(f)
		}
	}

	wg.Wait()

	return nil
}

func atoi(s string) int64 {
	i, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0
	}
	return i
}

func (ki *KubernetesInventory) convertQuantity(s string, m float64) int64 {
	q, err := resource.ParseQuantity(s)
	if err != nil {
		ki.Log.Debugf("failed to parse quantity: %s", err.Error())
		return 0
	}
	f, err := strconv.ParseFloat(fmt.Sprint(q.AsDec()), 64)
	if err != nil {
		ki.Log.Debugf("failed to parse float: %s", err.Error())
		return 0
	}
	if m < 1 {
		m = 1
	}
	return int64(f * m)
}

func (ki *KubernetesInventory) queryPodsFromKubelet(url string, v interface{}) error {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return fmt.Errorf("creating new http request for url %s failed: %w", url, err)
	}

	req.Header.Add("Accept", "application/json")
	resp, err := ki.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("error making HTTP request to %q: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s returned HTTP status %s", url, resp.Status)
	}

	if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
		return fmt.Errorf("error parsing response: %w", err)
	}

	return nil
}

func (ki *KubernetesInventory) createSelectorFilters() error {
	selectorFilter, err := filter.NewIncludeExcludeFilter(ki.SelectorInclude, ki.SelectorExclude)
	if err != nil {
		return err
	}
	ki.selectorFilter = selectorFilter
	return nil
}

func init() {
	inputs.Add("kube_inventory", func() telegraf.Input {
		return &KubernetesInventory{
			ResponseTimeout: config.Duration(time.Second * 5),
			Namespace:       "default",
			SelectorInclude: make([]string, 0),
			SelectorExclude: []string{"*"},
		}
	})
}
