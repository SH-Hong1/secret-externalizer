package main

import "testing"

func TestLoadConfig(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want string
	}{
		{"defaults", nil, ":8080"},
		{"addr", map[string]string{"WEB_ADDR": "127.0.0.1:9000"}, "127.0.0.1:9000"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			getenv := func(k string) string { return tt.env[k] }
			got, err := loadConfig(nil, getenv)
			if err != nil {
				t.Fatalf("loadConfig(nil, env %v) error = %v", tt.env, err)
			}
			if got.addr != tt.want {
				t.Errorf("loadConfig(nil, env %v).addr = %q, want %q", tt.env, got.addr, tt.want)
			}
		})
	}
}
