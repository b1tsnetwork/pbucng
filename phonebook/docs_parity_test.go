package main

import (
	"encoding/csv"
	"os"
	"strings"
	"testing"
)

// Die UCNG-Paritätsmatrix (PH-00 ff.) muss maschinenlesbar bleiben: feste Spalten, eindeutige
// Funktionen, bekannte Statuswerte. Fehlende Funktionen dürfen nicht stillschweigend verschwinden –
// dafür sorgt die Mindestanzahl (nur bewusst und mit Begründung im PR senken).
func TestUCNGParityMatrix(t *testing.T) {
	f, err := os.Open("docs/ucng-feature-parity.csv")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	rows, err := csv.NewReader(f).ReadAll()
	if err != nil {
		t.Fatalf("CSV nicht lesbar: %v", err)
	}
	want := []string{"Feature", "Quellmodule", "Standalone-Test", "Phonebook-Modus-Test", "Aktivierungsschalter", "Status", "Bemerkung"}
	if len(rows) == 0 || strings.Join(rows[0], "|") != strings.Join(want, "|") {
		t.Fatalf("Kopfzeile: %v", rows[0])
	}
	const minFeatures = 79 // Stand PH-00
	if len(rows)-1 < minFeatures {
		t.Fatalf("nur %d Funktionen – es dürfen keine Einträge verschwinden (mindestens %d)", len(rows)-1, minFeatures)
	}
	status := map[string]bool{"vorhanden": true, "nicht vorhanden": true, "angepasst": true, "integriert": true, "entfernt (begründet)": true}
	seen := map[string]bool{}
	for i, r := range rows[1:] {
		if r[0] == "" || r[1] == "" {
			t.Errorf("Zeile %d: Feature und Quellmodule sind Pflicht", i+2)
		}
		if seen[r[0]] {
			t.Errorf("Zeile %d: Feature doppelt: %s", i+2, r[0])
		}
		seen[r[0]] = true
		if !status[r[5]] {
			t.Errorf("Zeile %d: unbekannter Status %q", i+2, r[5])
		}
		// Release Candidate: keine Zeile mehr mit offener Phase (PH-xx)
		if strings.Contains(r[3], "offen (PH-") {
			t.Errorf("Zeile %d: %s – Phase noch offen: %s", i+2, r[0], r[3])
		}
		if r[5] == "entfernt (begründet)" && strings.TrimSpace(r[6]) == "" {
			t.Errorf("Zeile %d: Entfernen braucht eine Begründung", i+2)
		}
	}
}
