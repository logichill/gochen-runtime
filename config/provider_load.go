package config

import (
	"sort"

	"gochen/errors"
)

type configEntry struct {
	key               string
	value             any
	priority          int
	sequence          int
	environment       bool
	environmentPrefix string
}

type environmentConfigSource interface {
	IConfigSource
	environmentConfigPrefix() string
}

// load 按优先级顺序加载并合并所有配置源。
func (p *Provider) load() error {
	for _, source := range p.sources {
		if source == nil {
			return errors.NewCode(errors.InvalidInput, "config source must not be nil")
		}
	}

	// 按优先级排序（低优先级先加载，高优先级后覆盖）
	sortedSources := make([]IConfigSource, len(p.sources))
	copy(sortedSources, p.sources)

	sort.SliceStable(sortedSources, func(i, j int) bool {
		return sortedSources[i].Priority() < sortedSources[j].Priority()
	})

	// 依次加载配置源
	sequence := 0
	for _, source := range sortedSources {
		settings, err := source.Load()
		if err != nil {
			return errors.Wrap(err, errors.Dependency, "failed to load config source").
				WithContext("source", source.Name())
		}

		envSource, environment := source.(environmentConfigSource)
		environmentPrefix := ""
		if environment {
			environmentPrefix = envSource.environmentConfigPrefix()
		}

		// 合并配置，并保留来源元数据供结构体字段别名按真实优先级决胜。
		for k, v := range settings {
			p.settings[k] = v
			p.entries = append(p.entries, configEntry{
				key:               k,
				value:             v,
				priority:          source.Priority(),
				sequence:          sequence,
				environment:       environment,
				environmentPrefix: environmentPrefix,
			})
			sequence++
		}
	}

	return nil
}
