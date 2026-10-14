package scanner

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"sort"
	"strconv"
	"strings"
)

// DefaultPort is used when the port field is left empty.
const DefaultPort = 80

// maxPorts caps a single port specification so a typo like 1-65535 cannot
// flood the result table.
const maxPorts = 2048

// Target is one host:port pair to probe.
type Target struct {
	Host string `json:"host"`
	Port int    `json:"port"`
}

// Key returns a stable identifier for the target.
func (t Target) Key() string {
	return strings.ToLower(t.Host) + ":" + strconv.Itoa(t.Port)
}

// Addr returns the address passed to the dialer.
func (t Target) Addr() string {
	return net.JoinHostPort(t.Host, strconv.Itoa(t.Port))
}

// ParsePorts turns "80,443,8000-8010" into a sorted, de-duplicated port list.
func ParsePorts(spec string) ([]int, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return []int{DefaultPort}, nil
	}

	fields := strings.FieldsFunc(spec, func(r rune) bool {
		// ASCII separators plus the full-width forms users paste from chat apps.
		return r == ',' || r == ';' || r == ' ' || r == '\t' || r == '\uFF0C' || r == '\u3001' || r == '\u3000'
	})

	seen := make(map[int]bool)
	ports := make([]int, 0, len(fields))
	add := func(p int) {
		if !seen[p] {
			seen[p] = true
			ports = append(ports, p)
		}
	}

	for _, field := range fields {
		field = strings.TrimSpace(field)
		if field == "" {
			continue
		}
		if lo, hi, ok := strings.Cut(field, "-"); ok {
			low, err := parsePort(lo)
			if err != nil {
				return nil, err
			}
			high, err := parsePort(hi)
			if err != nil {
				return nil, err
			}
			if low > high {
				low, high = high, low
			}
			if high-low+1 > maxPorts {
				return nil, fmt.Errorf("端口范围 %s 过大，单次最多 %d 个端口", field, maxPorts)
			}
			for p := low; p <= high; p++ {
				add(p)
			}
			continue
		}
		p, err := parsePort(field)
		if err != nil {
			return nil, err
		}
		add(p)
	}

	if len(ports) == 0 {
		return []int{DefaultPort}, nil
	}
	if len(ports) > maxPorts {
		return nil, fmt.Errorf("单次最多检测 %d 个端口，当前 %d 个", maxPorts, len(ports))
	}
	sort.Ints(ports)
	return ports, nil
}

func parsePort(s string) (int, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, errors.New("端口不能为空")
	}
	p, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("端口 %q 不是数字", s)
	}
	if p < 1 || p > 65535 {
		return 0, fmt.Errorf("端口 %d 超出范围（1-65535）", p)
	}
	return p, nil
}

// ParseTargets combines the target lines with the port specification.
//
// Accepted line formats: host, host:port, host port, [::1], [::1]:443, ::1.
// A port written on the line itself wins over the shared port field.
func ParseTargets(targetText, portSpec string) ([]Target, []string, error) {
	ports, err := ParsePorts(portSpec)
	if err != nil {
		return nil, nil, err
	}

	text := strings.ReplaceAll(targetText, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	lines := strings.Split(text, "\n")

	seen := make(map[string]bool)
	targets := make([]Target, 0, len(lines))
	var warnings []string

	for i, raw := range lines {
		line := strings.TrimSpace(stripComment(raw))
		if line == "" {
			continue
		}
		host, linePort, err := splitHostPort(line)
		if err != nil {
			return nil, nil, fmt.Errorf("第 %d 行「%s」：%w", i+1, line, err)
		}

		usePorts := ports
		if linePort > 0 {
			usePorts = []int{linePort}
		}
		for _, p := range usePorts {
			t := Target{Host: host, Port: p}
			key := t.Key()
			if seen[key] {
				continue
			}
			seen[key] = true
			targets = append(targets, t)
		}
	}

	if len(targets) == 0 {
		return nil, nil, errors.New("请至少输入一个目标地址")
	}
	if len(targets) > 5000 {
		warnings = append(warnings, fmt.Sprintf("本次共 %d 个目标，建议提高并发或减少探测次数", len(targets)))
	}
	return targets, warnings, nil
}

func stripComment(line string) string {
	if i := strings.Index(line, "#"); i >= 0 {
		return line[:i]
	}
	return line
}

func splitHostPort(line string) (string, int, error) {
	// Bracketed IPv6, with or without a port: [::1] / [::1]:443
	if strings.HasPrefix(line, "[") {
		end := strings.Index(line, "]")
		if end < 0 {
			return "", 0, errors.New("IPv6 地址缺少右括号 ]")
		}
		host := strings.TrimSpace(line[1:end])
		if host == "" {
			return "", 0, errors.New("地址为空")
		}
		rest := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line[end+1:]), ":"))
		if rest == "" {
			return host, 0, nil
		}
		p, err := parsePort(rest)
		if err != nil {
			return "", 0, err
		}
		return host, p, nil
	}

	// Space separated: "host port"
	if fields := strings.Fields(line); len(fields) > 1 {
		if len(fields) > 2 {
			return "", 0, errors.New("格式无法识别，多个字段请写成「主机 端口」")
		}
		p, err := parsePort(fields[1])
		if err != nil {
			return "", 0, err
		}
		return fields[0], p, nil
	}

	// A lone IPv6 address contains several colons and carries no port.
	if strings.Count(line, ":") > 1 {
		addr, err := netip.ParseAddr(line)
		if err != nil {
			return "", 0, errors.New("看起来像 IPv6 地址，但无法解析；带端口时请写成 [地址]:端口")
		}
		return addr.String(), 0, nil
	}

	if host, portText, ok := strings.Cut(line, ":"); ok {
		host = strings.TrimSpace(host)
		if host == "" {
			return "", 0, errors.New("主机名为空")
		}
		p, err := parsePort(portText)
		if err != nil {
			return "", 0, err
		}
		return host, p, nil
	}

	return line, 0, nil
}
