package migration

import "testing"

func TestDecimalCapUsesExactTenPercentBoundary(t *testing.T) {
	baseline, err := parseDecimal("0.10")
	if err != nil {
		t.Fatal(err)
	}
	boundary, err := parseDecimal("0.11")
	if err != nil {
		t.Fatal(err)
	}
	justOver, err := parseDecimal("0.1100001")
	if err != nil {
		t.Fatal(err)
	}
	cap := maximumApprovedDecimal(baseline)
	if got := decimalString(cap); got != "0.11" || compareDecimal(boundary, cap) != 0 {
		t.Fatalf("cap = %s, want exact 0.11", got)
	}
	if compareDecimal(justOver, cap) <= 0 {
		t.Fatalf("price just over cap was accepted: %s", decimalString(justOver))
	}
}

func TestDecimalAddsServerAndPrimaryIPWithoutFloatingPoint(t *testing.T) {
	server, err := parseDecimal("0.0900")
	if err != nil {
		t.Fatal(err)
	}
	ip, err := parseDecimal("0.0200")
	if err != nil {
		t.Fatal(err)
	}
	if got := decimalString(addDecimal(server, ip)); got != "0.11" {
		t.Fatalf("server plus primary IP = %s, want 0.11", got)
	}
}
