package finding_test

import (
	"testing"

	"github.com/SH-Hong1/secret-externalizer/internal/finding"
)

func TestCustomerDataPath(t *testing.T) {
	tests := []struct {
		rel  string
		want bool
	}{
		{"app.log", true},
		{"logs/app.log", true},
		{"export/orders.CSV", true},
		{"a.tsv", true},
		{"readme.txt", true},
		{"db/init.sql", true},
		{"x.dat", true},
		{"conf/settings.json", true},
		{"log/x.properties", true},
		{"logs/x.java", true},
		{"a/b/Sample/x.xml", true},
		{"samples/x.yml", true},
		{"data/x.properties", true},
		{"deep/er/dump/x", true},
		{"LOGS/x", true},
		{"src/main/java/A.java", false},
		{"conf/db.properties", false},
		{"web.xml", false},
		{"app.log.1", true},
		{"catalina.out", true},
		{"nohup.OUT", true},
		{"app.log.2026-11-01", true},
		{"app.log-20261101", true},
		{"app.log_3", true},
		{"catalina.out.2", true},
		{"app.log.old", true},
		{"app.log.bak", true},
		{"a/app.log.1/x.java", false},
		{"log4j.log.properties", false},
		{"report.out.xml", false},
		{"x.log4j", false},
		{"x.output", false},
		{"app.log.", false},
		{"app.log.x1", false},
		{"logs", false},
		{"data", false},
		{"logsx/a.java", false},
		{"mydata/a.java", false},
		{"data.properties", false},
		{"src/log.java", false},
		{"a.jsonp", false},
	}
	for _, tt := range tests {
		if got := finding.CustomerDataPath(tt.rel); got != tt.want {
			t.Errorf("CustomerDataPath(%q) = %v, want %v", tt.rel, got, tt.want)
		}
	}
}
