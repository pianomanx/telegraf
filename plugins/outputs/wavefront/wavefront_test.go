package wavefront

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/influxdata/telegraf"
	"github.com/influxdata/telegraf/config"
	"github.com/influxdata/telegraf/metric"
	"github.com/influxdata/telegraf/plugins/outputs"
	serializers_wavefront "github.com/influxdata/telegraf/plugins/serializers/wavefront"
	"github.com/influxdata/telegraf/testutil"
)

// default config used by Tests
func defaultWavefront() *Wavefront {
	return &Wavefront{
		URL:             "http://localhost:2878",
		Prefix:          "testWF.",
		SimpleFields:    false,
		MetricSeparator: ".",
		ConvertPaths:    true,
		ConvertBool:     true,
		UseRegex:        false,
		Log:             testutil.Logger{},
	}
}

func TestBuildMetrics(t *testing.T) {
	w := defaultWavefront()
	w.Prefix = "testthis."

	pathReplacer = strings.NewReplacer("_", w.MetricSeparator)

	testMetric1 := metric.New(
		"test.simple.metric",
		map[string]string{"tag1": "value1", "host": "testHost"},
		map[string]interface{}{"value": 123},
		time.Date(2009, time.November, 10, 23, 0, 0, 0, time.UTC),
	)

	var timestamp int64 = 1257894000

	var metricTests = []struct {
		metric       telegraf.Metric
		metricPoints []serializers_wavefront.MetricPoint
	}{
		{
			testutil.TestMetric(float64(1), "testing_just*a%metric:float", "metric2"),
			[]serializers_wavefront.MetricPoint{
				{Metric: w.Prefix + "testing.just-a-metric-float", Value: 1, Timestamp: timestamp, Tags: map[string]string{"tag1": "value1"}},
				{Metric: w.Prefix + "testing.metric2", Value: 1, Timestamp: timestamp, Tags: map[string]string{"tag1": "value1"}},
			},
		},
		{
			testutil.TestMetric(float64(1), "testing_just/another,metric:float", "metric2"),
			[]serializers_wavefront.MetricPoint{
				{Metric: w.Prefix + "testing.just-another-metric-float", Value: 1, Timestamp: timestamp, Tags: map[string]string{"tag1": "value1"}},
				{Metric: w.Prefix + "testing.metric2", Value: 1, Timestamp: timestamp, Tags: map[string]string{"tag1": "value1"}},
			},
		},
		{
			testMetric1,
			[]serializers_wavefront.MetricPoint{
				{Metric: w.Prefix + "test.simple.metric", Value: 123, Timestamp: timestamp, Source: "testHost", Tags: map[string]string{"tag1": "value1"}},
			},
		},
	}

	for _, mt := range metricTests {
		ml := w.buildMetrics(mt.metric)
		for i, line := range ml {
			if mt.metricPoints[i].Metric != line.Metric || mt.metricPoints[i].Value != line.Value {
				t.Errorf("\nexpected\t%+v %+v\nreceived\t%+v %+v\n", mt.metricPoints[i].Metric, mt.metricPoints[i].Value, line.Metric, line.Value)
			}
		}
	}
}

func TestBuildMetricsStrict(t *testing.T) {
	w := defaultWavefront()
	w.Prefix = "testthis."
	w.UseStrict = true

	pathReplacer = strings.NewReplacer("_", w.MetricSeparator)

	var timestamp int64 = 1257894000

	var metricTests = []struct {
		metric       telegraf.Metric
		metricPoints []serializers_wavefront.MetricPoint
	}{
		{
			testutil.TestMetric(float64(1), "testing_just*a%metric:float", "metric2"),
			[]serializers_wavefront.MetricPoint{
				{Metric: w.Prefix + "testing.just-a-metric-float", Value: 1, Timestamp: timestamp, Tags: map[string]string{"tag1": "value1"}},
				{Metric: w.Prefix + "testing.metric2", Value: 1, Timestamp: timestamp, Tags: map[string]string{"tag1": "value1"}},
			},
		},
		{
			testutil.TestMetric(float64(1), "testing_just/another,metric:float", "metric2"),
			[]serializers_wavefront.MetricPoint{
				{
					Metric:    w.Prefix + "testing.just/another,metric-float",
					Value:     1,
					Timestamp: timestamp,
					Tags:      map[string]string{"tag/1": "value1", "tag,2": "value2"},
				},
				{Metric: w.Prefix + "testing.metric2", Value: 1, Timestamp: timestamp, Tags: map[string]string{"tag/1": "value1", "tag,2": "value2"}},
			},
		},
	}

	for _, mt := range metricTests {
		ml := w.buildMetrics(mt.metric)
		for i, line := range ml {
			if mt.metricPoints[i].Metric != line.Metric || mt.metricPoints[i].Value != line.Value {
				t.Errorf("\nexpected\t%+v %+v\nreceived\t%+v %+v\n", mt.metricPoints[i].Metric, mt.metricPoints[i].Value, line.Metric, line.Value)
			}
		}
	}
}

func TestBuildMetricsWithSimpleFields(t *testing.T) {
	w := defaultWavefront()
	w.Prefix = "testthis."
	w.SimpleFields = true

	pathReplacer = strings.NewReplacer("_", w.MetricSeparator)

	testMetric1 := metric.New(
		"test.simple.metric",
		map[string]string{"tag1": "value1"},
		map[string]interface{}{"value": 123},
		time.Date(2009, time.November, 10, 23, 0, 0, 0, time.UTC),
	)

	var metricTests = []struct {
		metric      telegraf.Metric
		metricLines []serializers_wavefront.MetricPoint
	}{
		{
			testutil.TestMetric(float64(1), "testing_just*a%metric:float"),
			[]serializers_wavefront.MetricPoint{{Metric: w.Prefix + "testing.just-a-metric-float.value", Value: 1}},
		},
		{
			testMetric1,
			[]serializers_wavefront.MetricPoint{{Metric: w.Prefix + "test.simple.metric.value", Value: 123}},
		},
	}

	for _, mt := range metricTests {
		ml := w.buildMetrics(mt.metric)
		for i, line := range ml {
			if mt.metricLines[i].Metric != line.Metric || mt.metricLines[i].Value != line.Value {
				t.Errorf("\nexpected\t%+v %+v\nreceived\t%+v %+v\n", mt.metricLines[i].Metric, mt.metricLines[i].Value, line.Metric, line.Value)
			}
		}
	}
}

func TestBuildTags(t *testing.T) {
	w := defaultWavefront()

	var tagtests = []struct {
		ptIn      map[string]string
		outSource string
		outTags   map[string]string
	}{
		{
			map[string]string{},
			"",
			map[string]string{},
		},
		{
			map[string]string{"one": "two", "three": "four", "host": "testHost"},
			"testHost",
			map[string]string{"one": "two", "three": "four"},
		},
		{
			map[string]string{"aaa": "bbb", "host": "testHost"},
			"testHost",
			map[string]string{"aaa": "bbb"},
		},
		{
			map[string]string{"bbb": "789", "aaa": "123", "host": "testHost"},
			"testHost",
			map[string]string{"aaa": "123", "bbb": "789"},
		},
		{
			map[string]string{"host": "aaa", "dc": "bbb"},
			"aaa",
			map[string]string{"dc": "bbb"},
		},
		{
			map[string]string{"host": "aaa", "dc": "a*$a\\abbb\"som/et|hing else", "bad#k%e/y that*sho\\uld work": "value1"},
			"aaa",
			map[string]string{"dc": "a-$a\\abbb\"som/et|hing else", "bad-k-e-y-that-sho-uld-work": "value1"},
		},
	}

	for _, tt := range tagtests {
		source, tags := w.buildTags(tt.ptIn)
		if source != tt.outSource {
			t.Errorf("\nexpected\t%+v\nreceived\t%+v\n", tt.outSource, source)
		}
		if !reflect.DeepEqual(tags, tt.outTags) {
			t.Errorf("\nexpected\t%+v\nreceived\t%+v\n", tt.outTags, tags)
		}
	}
}

func TestBuildTagsWithSource(t *testing.T) {
	w := defaultWavefront()
	w.SourceOverride = []string{"snmp_host", "hostagent"}

	var tagtests = []struct {
		ptIn      map[string]string
		outSource string
		outTags   map[string]string
	}{
		{
			map[string]string{"host": "realHost"},
			"realHost",
			map[string]string{},
		},
		{
			map[string]string{"tag1": "value1", "host": "realHost"},
			"realHost",
			map[string]string{"tag1": "value1"},
		},
		{
			map[string]string{"snmp_host": "realHost", "host": "origHost"},
			"realHost",
			map[string]string{"telegraf_host": "origHost"},
		},
		{
			map[string]string{"hostagent": "realHost", "host": "origHost"},
			"realHost",
			map[string]string{"telegraf_host": "origHost"},
		},
		{
			map[string]string{"hostagent": "abc", "snmp_host": "realHost", "host": "origHost"},
			"realHost",
			map[string]string{"hostagent": "abc", "telegraf_host": "origHost"},
		},
		{
			map[string]string{"something": "abc", "host": "r*@l\"Ho/st"},
			"r-@l\"Ho/st",
			map[string]string{"something": "abc"},
		},
		{
			map[string]string{"hostagent": "realHost", "env": "qa", "tag": "val"},
			"realHost",
			map[string]string{"env": "qa", "tag": "val"},
		},
	}

	for _, tt := range tagtests {
		source, tags := w.buildTags(tt.ptIn)
		if source != tt.outSource {
			t.Errorf("\nexpected\t%+v\nreceived\t%+v\n", tt.outSource, source)
		}
		if !reflect.DeepEqual(tags, tt.outTags) {
			t.Errorf("\nexpected\t%+v\nreceived\t%+v\n", tt.outTags, tags)
		}
	}
}

func TestBuildValue(t *testing.T) {
	w := defaultWavefront()

	var valuetests = []struct {
		value interface{}
		name  string
		out   float64
		isErr bool
	}{
		{value: int64(123), out: 123},
		{value: uint64(456), out: 456},
		{value: float64(789), out: 789},
		{value: true, out: 1},
		{value: false, out: 0},
		{value: "bad", out: 0, isErr: true},
	}

	for _, vt := range valuetests {
		value, err := buildValue(vt.value, vt.name, w)
		if vt.isErr && err == nil {
			t.Errorf("\nexpected error with\t%+v\nreceived\t%+v\n", vt.out, value)
		} else if value != vt.out {
			t.Errorf("\nexpected\t%+v\nreceived\t%+v\n", vt.out, value)
		}
	}
}

func TestTagLimits(t *testing.T) {
	w := defaultWavefront()
	w.TruncateTags = true

	// Should fail (all tags skipped)
	template := make(map[string]string)
	template[strings.Repeat("x", 255)] = "whatever"
	_, tags := w.buildTags(template)
	require.Empty(t, tags, "All tags should have been skipped")

	// Should truncate value
	template = make(map[string]string)
	longKey := strings.Repeat("x", 253)
	template[longKey] = "whatever"
	_, tags = w.buildTags(template)
	require.Contains(t, tags, longKey, "Should contain truncated long key")
	require.Equal(t, "w", tags[longKey])

	// Should not truncate
	template = make(map[string]string)
	longKey = strings.Repeat("x", 251)
	template[longKey] = "Hi!"
	_, tags = w.buildTags(template)
	require.Contains(t, tags, longKey, "Should contain non truncated long key")
	require.Equal(t, "Hi!", tags[longKey])

	// Turn off truncating and make sure it leaves the tags intact
	w.TruncateTags = false
	template = make(map[string]string)
	longKey = strings.Repeat("x", 255)
	template[longKey] = longKey
	_, tags = w.buildTags(template)
	require.Contains(t, tags, longKey, "Should contain non truncated long key")
	require.Equal(t, longKey, tags[longKey])
}

func TestParseConnectionUrlReturnsAnErrorForInvalidUrls(t *testing.T) {
	w := &Wavefront{
		URL: "invalid url",
		Log: testutil.Logger{},
	}
	_, err := w.parseConnectionURL()
	require.EqualError(t, err, "could not parse the provided URL: invalid url")
}
func TestParseConnectionUrlReturnsAllowsTokensInUrl(t *testing.T) {
	w := &Wavefront{
		URL: "https://11111111-2222-3333-4444-555555555555@surf.wavefront.com",
		Log: testutil.Logger{},
	}

	url, err := w.parseConnectionURL()
	require.NoError(t, err)
	require.Equalf(t, "https://11111111-2222-3333-4444-555555555555@surf.wavefront.com", url, "Token value should not overwrite the token embedded in url")
}

func TestParseConnectionUrlUsesHostAndPortWhenUrlIsOmitted(t *testing.T) {
	w := &Wavefront{
		URL: "http://surf.wavefront.com:8080",
		Log: testutil.Logger{},
	}

	url, err := w.parseConnectionURL()
	require.NoError(t, err)
	require.Equalf(t, "http://surf.wavefront.com:8080", url, "Should combine host and port into URI")
}

func TestDefaults(t *testing.T) {
	defaultWavefront := outputs.Outputs["wavefront"]().(*Wavefront)
	require.Equal(t, 10000, defaultWavefront.HTTPMaximumBatchSize)
	require.Equal(t, config.Duration(10*time.Second), defaultWavefront.Timeout)
	require.Empty(t, defaultWavefront.TLSCA)
}

func TestMakeAuthOptions(t *testing.T) {
	cspAPIWavefront := outputs.Outputs["wavefront"]().(*Wavefront)
	cspAPIWavefront.AuthCSPAPIToken = config.NewSecret([]byte("fake-app-token"))
	options, err := cspAPIWavefront.makeAuthOptions()
	require.NoError(t, err)
	require.Len(t, options, 1)

	cspClientCredsWavefront := outputs.Outputs["wavefront"]().(*Wavefront)
	cspClientCredsWavefront.AuthCSPClientCredentials = &authCSPClientCredentials{
		AppID:     config.NewSecret([]byte("fake-app-id")),
		AppSecret: config.NewSecret([]byte("fake-app-secret")),
	}
	options, err = cspClientCredsWavefront.makeAuthOptions()
	require.NoError(t, err)
	require.Len(t, options, 1)

	orgID := "org-id"
	cspClientCredsWithOrgIDWavefront := outputs.Outputs["wavefront"]().(*Wavefront)
	cspClientCredsWithOrgIDWavefront.AuthCSPClientCredentials = &authCSPClientCredentials{
		AppID:     config.NewSecret([]byte("fake-app-id")),
		AppSecret: config.NewSecret([]byte("fake-app-secret")),
		OrgID:     &orgID,
	}
	options, err = cspClientCredsWithOrgIDWavefront.makeAuthOptions()
	require.NoError(t, err)
	require.Len(t, options, 1)

	apiTokenWavefront := outputs.Outputs["wavefront"]().(*Wavefront)
	apiTokenWavefront.AuthCSPAPIToken = config.NewSecret([]byte("fake-wavefront-api-token"))
	options, err = apiTokenWavefront.makeAuthOptions()
	require.NoError(t, err)
	require.Len(t, options, 1)

	noAuthOptionsWavefront := outputs.Outputs["wavefront"]().(*Wavefront)
	options, err = noAuthOptionsWavefront.makeAuthOptions()
	require.NoError(t, err)
	require.Empty(t, options)
}

// Benchmarks to test performance of string replacement via Regex and Sanitize
var testString = "this_is*my!test/string\\for=replacement"

func BenchmarkReplaceAllString(b *testing.B) {
	for n := 0; n < b.N; n++ {
		sanitizedRegex.ReplaceAllString(testString, "-")
	}
}

func BenchmarkReplaceAllLiteralString(b *testing.B) {
	for n := 0; n < b.N; n++ {
		sanitizedRegex.ReplaceAllLiteralString(testString, "-")
	}
}

func BenchmarkReplacer(b *testing.B) {
	for n := 0; n < b.N; n++ {
		serializers_wavefront.Sanitize(false, testString)
	}
}
