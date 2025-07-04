//go:generate ../../../tools/readme_config_includer/generator
package enum

import (
	_ "embed"
	"fmt"
	"strconv"

	"github.com/influxdata/telegraf"
	"github.com/influxdata/telegraf/filter"
	"github.com/influxdata/telegraf/plugins/processors"
)

//go:embed sample.conf
var sampleConfig string

type Enum struct {
	Mappings []*mapping `toml:"mapping"`
}

type mapping struct {
	Tag     string      `toml:"tag" deprecated:"1.35.0;1.40.0;use 'tags' instead"`
	Field   string      `toml:"field" deprecated:"1.35.0;1.40.0;use 'fields' instead"`
	Tags    []string    `toml:"tags"`
	Fields  []string    `toml:"fields"`
	Dest    string      `toml:"dest"`
	Default interface{} `toml:"default"`

	fieldFilter filter.Filter
	tagFilter   filter.Filter

	ValueMappings map[string]interface{}
}

func (*Enum) SampleConfig() string {
	return sampleConfig
}

func (mapper *Enum) Init() error {
	for _, mapping := range mapper.Mappings {
		// Handle deprecated field option
		if mapping.Field != "" {
			mapping.Fields = append(mapping.Fields, mapping.Field)
		}

		fieldFilter, err := filter.Compile(mapping.Fields)
		if err != nil {
			return fmt.Errorf("failed to create new field filter: %w", err)
		}
		mapping.fieldFilter = fieldFilter

		// Handle deprecated tag option
		if mapping.Tag != "" {
			mapping.Tags = append(mapping.Tags, mapping.Tag)
		}

		tagFilter, err := filter.Compile(mapping.Tags)
		if err != nil {
			return fmt.Errorf("failed to create new tag filter: %w", err)
		}
		mapping.tagFilter = tagFilter
	}

	return nil
}

func (mapper *Enum) Apply(in ...telegraf.Metric) []telegraf.Metric {
	for i := 0; i < len(in); i++ {
		in[i] = mapper.applyMappings(in[i])
	}
	return in
}

func (mapper *Enum) applyMappings(metric telegraf.Metric) telegraf.Metric {
	newFields := make(map[string]interface{})
	newTags := make(map[string]string)

	for _, mapping := range mapper.Mappings {
		if mapping.fieldFilter != nil {
			fieldMapping(metric, mapping, newFields)
		}
		if mapping.tagFilter != nil {
			tagMapping(metric, mapping, newTags)
		}
	}

	for k, v := range newFields {
		writeField(metric, k, v)
	}

	for k, v := range newTags {
		writeTag(metric, k, v)
	}

	return metric
}

func fieldMapping(metric telegraf.Metric, mapping *mapping, newFields map[string]interface{}) {
	fields := metric.FieldList()
	for _, f := range fields {
		if !mapping.fieldFilter.Match(f.Key) {
			continue
		}
		if adjustedValue, isString := adjustValue(f.Value).(string); isString {
			if mappedValue, isMappedValuePresent := mapping.mapValue(adjustedValue); isMappedValuePresent {
				newFields[mapping.getDestination(f.Key)] = mappedValue
			}
		}
	}
}

func tagMapping(metric telegraf.Metric, mapping *mapping, newTags map[string]string) {
	tags := metric.TagList()
	for _, t := range tags {
		if !mapping.tagFilter.Match(t.Key) {
			continue
		}
		if mappedValue, isMappedValuePresent := mapping.mapValue(t.Value); isMappedValuePresent {
			switch val := mappedValue.(type) {
			case string:
				newTags[mapping.getDestination(t.Key)] = val
			default:
				newTags[mapping.getDestination(t.Key)] = fmt.Sprintf("%v", val)
			}
		}
	}
}

func adjustValue(in interface{}) interface{} {
	switch val := in.(type) {
	case bool:
		return strconv.FormatBool(val)
	case int64:
		return strconv.FormatInt(val, 10)
	case float64:
		return strconv.FormatFloat(val, 'f', -1, 64)
	case uint64:
		return strconv.FormatUint(val, 10)
	default:
		return in
	}
}

func (mapping *mapping) mapValue(original string) (interface{}, bool) {
	if mapped, found := mapping.ValueMappings[original]; found {
		return mapped, true
	}
	if mapping.Default != nil {
		return mapping.Default, true
	}
	return original, false
}

func (mapping *mapping) getDestination(defaultDest string) string {
	if mapping.Dest != "" {
		return mapping.Dest
	}
	return defaultDest
}

func writeField(metric telegraf.Metric, name string, value interface{}) {
	metric.RemoveField(name)
	metric.AddField(name, value)
}

func writeTag(metric telegraf.Metric, name, value string) {
	metric.RemoveTag(name)
	metric.AddTag(name, value)
}

func init() {
	processors.Add("enum", func() telegraf.Processor {
		return &Enum{}
	})
}
