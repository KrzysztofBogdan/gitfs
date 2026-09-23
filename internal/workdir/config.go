package workdir

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"sort"
	"strings"
)

type Config struct{ sections map[string]map[string]string }

func NewConfig() *Config { return &Config{sections: map[string]map[string]string{}} }

func ParseConfig(data []byte) (*Config, error) {
	c := NewConfig()
	sec := ""
	sc := bufio.NewScanner(bytes.NewReader(data))
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		switch {
		case line == "" || line[0] == '#' || line[0] == ';':
		case line[0] == '[' && line[len(line)-1] == ']':
			sec = strings.TrimSpace(line[1 : len(line)-1])
		default:
			k, v, ok := strings.Cut(line, "=")
			if !ok {
				return nil, fmt.Errorf("config line %d: want key = value", n)
			}
			c.Set(sec, strings.TrimSpace(k), strings.TrimSpace(v))
		}
	}
	return c, sc.Err()
}

func (c *Config) Get(section, key string) string { return c.sections[section][key] }

func (c *Config) Set(section, key, value string) {
	if c.sections[section] == nil {
		c.sections[section] = map[string]string{}
	}
	c.sections[section][key] = value
}

func (c *Config) Section(name string) map[string]string {
	out := map[string]string{}
	for k, v := range c.sections[name] {
		out[k] = v
	}
	return out
}

func (c *Config) Bytes() []byte {
	var names []string
	for n := range c.sections {
		if n != "remote" && n != "policy" {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	names = append([]string{"remote", "policy"}, names...)
	var b bytes.Buffer
	for _, n := range names {
		kv := c.sections[n]
		if len(kv) == 0 {
			continue
		}
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		fmt.Fprintf(&b, "[%s]\n", n)
		keys := make([]string, 0, len(kv))
		for k := range kv {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(&b, "%s = %s\n", k, kv[k])
		}
	}
	return b.Bytes()
}

func (t *Tree) LoadConfig() (*Config, error) {
	data, err := os.ReadFile(t.gfs("config"))
	if err != nil {
		return nil, err
	}
	return ParseConfig(data)
}

func (t *Tree) SaveConfig(c *Config) error { return writeAtomic(t.gfs("config"), c.Bytes()) }
