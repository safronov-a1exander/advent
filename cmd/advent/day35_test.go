package main

import (
	"strings"
	"testing"
)

func TestSplitQuotedPlan(t *testing.T) {
	got, err := splitQuoted(`pr create --base day-34 --head day-35 --title "Day 35 — сдача \"дня\"" --body-file reports/ship/day35-pr.md`)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"pr", "create", "--base", "day-34", "--head", "day-35", "--title", `Day 35 — сдача "дня"`, "--body-file", "reports/ship/day35-pr.md"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("%q", got)
	}
}
