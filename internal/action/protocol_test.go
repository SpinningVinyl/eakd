// SPDX-License-Identifier: GPL-2.0-or-later

package action

import (
	"strings"
	"testing"
)

func TestValidateIDLength(t *testing.T) {
	if err := ValidateID(strings.Repeat("a", MaxActionIDBytes)); err != nil {
		t.Fatalf("ValidateID rejected an ID at the limit: %v", err)
	}
	if err := ValidateID(strings.Repeat("a", MaxActionIDBytes+1)); err == nil {
		t.Fatal("ValidateID accepted an ID over the limit")
	}
	if err := ValidateID(strings.Repeat("é", MaxActionIDBytes/2+1)); err == nil {
		t.Fatal("ValidateID counted characters instead of encoded bytes")
	}
	if err := ValidateID(" \t"); err == nil {
		t.Fatal("ValidateID accepted a blank string")
	}
}
