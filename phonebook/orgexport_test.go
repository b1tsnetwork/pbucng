package main

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
)

func TestOrgExport(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	st := a.st()
	dez, _ := st.CreateOrgUnit(ctx, OrgUnit{Name: "Zentrale Verwaltung", Short: "I"})
	amt, _ := st.CreateOrgUnit(ctx, OrgUnit{Name: "Personal", Short: "10", Parent: dez, Contact: &ContactPref{Email: "personal@stadt.de"}})
	sg, _ := st.CreateOrgUnit(ctx, OrgUnit{Name: "Zentrale Dienste", Parent: amt})
	tagID, _ := st.CreateTag(ctx, "Amtsleitung", "funktion", "test")
	lv, _ := json.Marshal([]OrgLevel{{Name: "Dezernat"}, {Name: "Amt", LeaderTag: tagID}, {Name: "Sachgebiet"}})
	_ = st.SetSetting(ctx, settingOrgLevels, string(lv))

	mk := func(user, first, last, room string, unit int64) int64 {
		id, err := st.CreateEmployee(ctx, &EmployeeRec{Username: user, FirstName: first, LastName: last, Email: user + "@stadt.de", Room: room, Active: true, Source: "ad"})
		if err != nil {
			t.Fatal(err)
		}
		if unit != 0 {
			_, _ = st.SetOrgUnit(ctx, []int64{id}, unit)
		}
		return id
	}
	dezent := mk("d.chef", "Dora", "Chef", "A 0.01", dez)
	boss := mk("a.becker", "Anna", "Becker", "A 1.10", amt)
	worker := mk("k.richter", "Karl", "Richter", "A 1.12", sg)
	_ = mk("p.neu", "Paul", "Neu", "A 1.12", sg)
	hid := mk("x.geheim", "X", "Geheim", "", amt)
	h, _ := st.GetEmployee(ctx, hid)
	h.Hidden = true
	_ = st.UpdateEmployee(ctx, h)
	_ = st.SetDeputies(ctx, worker, []int64{boss})
	if _, err := st.SetEmployeeTags(ctx, boss, []TagInput{{Name: "Amtsleitung", Category: "funktion"}}, "test", false); err != nil {
		t.Fatal(err)
	}
	u, _ := st.ListOrgUnits(ctx)
	for _, x := range u {
		if x.ID == dez {
			x.Leaders = []int64{dezent}
			_ = st.UpdateOrgUnit(ctx, x)
		}
	}

	hd := a.routes(false)
	if rec := doReq(hd, "GET", "/api/export/org", "", "10.0.0.1", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("ohne Token konfiguriert: %d", rec.Code)
	}
	a.cfg.OrgExport.Token = "geheim"
	if rec := doReq(hd, "GET", "/api/export/org", "", "10.0.0.1", map[string]string{"Authorization": "Bearer falsch"}); rec.Code != http.StatusUnauthorized {
		t.Fatalf("falsches Token: %d", rec.Code)
	}
	rec := doReq(hd, "GET", "/api/export/org", "", "10.0.0.1", map[string]string{"Authorization": "Bearer geheim"})
	if rec.Code != 200 {
		t.Fatalf("Export: %d %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Format string         `json:"format"`
		Units  []exportUnit   `json:"units"`
		People []exportPerson `json:"people"`
		Rooms  []exportRoom   `json:"rooms"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Format != orgExportFormat || len(out.Units) != 3 || len(out.People) != 4 {
		t.Fatalf("Umfang: %s %d Einheiten, %d Personen", out.Format, len(out.Units), len(out.People))
	}
	byKey := map[string]exportUnit{}
	for _, x := range out.Units {
		byKey[x.Key] = x
	}
	if x := byKey["10"]; x.Level != "Amt" || len(x.Leaders) != 1 || x.Leaders[0].Username != "a.becker" || x.ParentKey != "I" || x.Email != "personal@stadt.de" || x.Path != "I – Zentrale Verwaltung › 10 – Personal" {
		t.Fatalf("Amt: %+v", x)
	}
	sgk := "OE-" + itoa(sg)
	if x := byKey[sgk]; x.Head == nil || x.Head.Username != "a.becker" || x.Email != "personal@stadt.de" || x.Level != "Sachgebiet" || x.Top != "I – Zentrale Verwaltung" {
		t.Fatalf("Sachgebiet erbt Leitung/Kontakt: %+v", x)
	}
	pp := map[string]exportPerson{}
	for _, p := range out.People {
		pp[p.Username] = p
	}
	if p := pp["k.richter"]; p.Manager != "a.becker" || p.UnitKey != sgk || len(p.Deputies) != 1 || p.Deputies[0] != "a.becker" {
		t.Fatalf("Richter: %+v", p)
	}
	if p := pp["a.becker"]; p.Manager != "d.chef" || p.Function != "Leitung Amt" {
		t.Fatalf("Leitung hat Oberheit als Vorgesetzte: %+v", p)
	}
	if _, ok := pp["x.geheim"]; ok {
		t.Fatal("ausgeblendeter Eintrag im Export")
	}
	if len(out.Rooms) != 3 || out.Rooms[2].Name != "A 1.12" || out.Rooms[2].People != 2 || out.Rooms[2].UnitKey != sgk {
		t.Fatalf("Räume: %+v", out.Rooms)
	}
	etag := rec.Header().Get("ETag")
	if r2 := doReq(hd, "GET", "/api/export/org", "", "10.0.0.1", map[string]string{"Authorization": "Bearer geheim", "If-None-Match": etag}); r2.Code != http.StatusNotModified {
		t.Fatalf("ETag: %d", r2.Code)
	}
	// geänderte Ebenen-Namen ergeben eine neue Version
	lv2, _ := json.Marshal([]OrgLevel{{Name: "Dezernat"}, {Name: "Fachbereich", LeaderTag: tagID}, {Name: "Sachgebiet"}})
	_ = st.SetSetting(ctx, settingOrgLevels, string(lv2))
	if r3 := doReq(hd, "GET", "/api/export/org", "", "10.0.0.1", map[string]string{"Authorization": "Bearer geheim", "If-None-Match": etag}); r3.Code != 200 || r3.Header().Get("ETag") == etag {
		t.Fatalf("Ebenen geändert: %d", r3.Code)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Fatalf("Cache-Control: %q", cc)
	}
}

func itoa(i int64) string { b, _ := json.Marshal(i); return string(b) }
