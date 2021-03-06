package pggateway

import (
	"github.com/go-yaml/yaml"
)

type Config struct {
	Procs     int                        `yaml:"procs,omitempty"`
	Logging   map[string]ConfigMap       `yaml:"logging,omitempty"`
	Listeners map[string]*ListenerConfig `yaml:"listeners,omitempty"`
}

type TargetConfig struct {
	Host    string `yaml:"host,omitempty"`
	Port    int    `yaml:"port,omitempty"`
	SSLMode string `yaml:"sslmode,omitempty"`
}

type SSLConfig struct {
	Enabled     bool   `yaml:"enabled,omitempty"`
	Required    bool   `yaml:"required,omitempty"`
	Certificate string `yaml:"certificate,omitempty"`
	Key         string `yaml:"key,omitempty"`
}

type ConfigMap map[string]interface{}

func (c ConfigMap) String(name string) (string, bool) {
	v, ok := c[name]
	if !ok {
		return "", false
	}

	s, ok := v.(string)
	if !ok {
		return "", false
	}
	return s, true
}

func (c ConfigMap) StringDefault(name string, d string) string {
	s, ok := c.String(name)
	if !ok {
		return d
	}
	return s
}

func (c ConfigMap) Bool(name string) (bool, bool) {
	v, ok := c[name]
	if !ok {
		return false, false
	}

	b, ok := v.(bool)
	if !ok {
		return false, false
	}
	return b, true
}

func (c ConfigMap) BoolDefault(name string, d bool) bool {
	b, ok := c.Bool(name)
	if !ok {
		return d
	}
	return b
}

func (c ConfigMap) Map(name string) (ConfigMap, bool) {
	raw, ok := c[name]
	if !ok {
		return nil, false
	}
	value, ok := raw.(map[interface{}]interface{})
	if !ok {
		return nil, false
	}

	m := make(ConfigMap)
	for k, v := range value {
		key, ok := k.(string)
		if !ok {
			return nil, false
		}
		m[key] = v
	}
	return m, true
}

type ListenerConfig struct {
	Bind           string               `yaml:"bind,omitempty"`
	SSL            SSLConfig            `yaml:"ssl,omitempty"`
	Target         TargetConfig         `yaml:"target,omitempty"`
	Authentication map[string]ConfigMap `yaml:"authentication,omitempty"`
	Logging        map[string]ConfigMap `yaml:"logging,omitempty"`
	Databases      map[string]ConfigMap `yaml:"databases,omitempty"`
}

func NewConfig() *Config {
	return &Config{
		Logging:   make(map[string]ConfigMap),
		Listeners: make(map[string]*ListenerConfig),
	}
}

func (c *Config) Unmarshal(in []byte) error {
	err := yaml.UnmarshalStrict(in, c)
	if err != nil {
		return err
	}

	return c.resolveListeners()
}

func (c *Config) resolveListeners() error {
	for bind, config := range c.Listeners {
		config.Bind = bind
	}

	return nil
}

func (c *Config) GetListeners() []*Listener {
	listeners := make([]*Listener, 0)
	for _, config := range c.Listeners {
		listeners = append(listeners, NewListener(config))
	}
	return listeners
}
