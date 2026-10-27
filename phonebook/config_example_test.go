package main

import "testing"

func TestExampleConfigLoads(t *testing.T) {
	c, err := loadConfig("config.example.json")
	if err != nil {
		t.Fatal(err)
	}
	if !c.metricsEnabled() || c.ClientLog.Enabled || c.ClientLog.RetentionDays != 90 || len(c.metricsAllow) != 6 {
		t.Fatalf("Beispielkonfiguration: %+v", c.Monitoring)
	}
}
