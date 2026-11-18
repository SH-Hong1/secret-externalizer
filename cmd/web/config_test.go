package main

import "testing"

func TestLoadConfig(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		want    string
		wantErr bool
	}{
		{name: "defaults", want: ":8080"},
		{name: "addr", env: map[string]string{"WEB_ADDR": "127.0.0.1:9000"}, want: "127.0.0.1:9000"},
		{name: "invalid addr", env: map[string]string{"WEB_ADDR": "bad"}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			getenv := func(k string) string { return tt.env[k] }
			got, err := loadConfig(nil, getenv)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("loadConfig(nil, env %v) error = nil, want an error", tt.env)
				}
				return
			}
			if err != nil {
				t.Fatalf("loadConfig(nil, env %v) error = %v", tt.env, err)
			}
			if got.addr != tt.want {
				t.Errorf("loadConfig(nil, env %v).addr = %q, want %q", tt.env, got.addr, tt.want)
			}
		})
	}
}
