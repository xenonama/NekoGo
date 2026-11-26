package main

import (
	"regexp"
	"strings"
	"testing"
)

// Proxies-tab grouping: which profiles a feed rename is allowed to move,
// whether every group keeps its header while the list is windowed, and
// whether a whole selection can be re-filed at once.

func groupsOf(store *ConfigStore) map[string]string {
	out := map[string]string{}
	for _, p := range store.ListProfiles() {
		out[p.Name] = p.Group
	}
	return out
}

func countInGroup(groups map[string]string, name string) int {
	n := 0
	for _, g := range groups {
		if g == name {
			n++
		}
	}
	return n
}

// TestFeedRenameMovesOnlyItsOwnProfiles: renaming a feed re-files the
// profiles it imported, because their group was stamped with the old name
// at import time. The loop matched on the group name alone, so a second
// feed sharing that display name — the usual thing when one provider
// hands out two links — and every hand-added profile the user filed under
// the same remark lost their group and joined the renamed feed. A refresh
// of the other feed then stamped its nodes back, splitting it in two.
func TestFeedRenameMovesOnlyItsOwnProfiles(t *testing.T) {
	bodyA := nodeLink(1) + "\n" + nodeLink(2)
	bodyB := nodeLink(3) + "\n" + nodeLink(4)
	srvA := subFeedServer(t, func() string { return bodyA })
	srvB := subFeedServer(t, func() string { return bodyB })
	store, err := NewConfigStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	app := NewApp(store, make(chan LogEntry, 256))
	if _, err := app.ImportLinks(srvA.URL); err != nil {
		t.Fatalf("import A: %v", err)
	}
	if _, err := app.ImportLinks(srvB.URL); err != nil {
		t.Fatalf("import B: %v", err)
	}
	// One provider, two links, one name.
	if _, err := app.EditSubscription(srvA.URL, srvA.URL, "Premium"); err != nil {
		t.Fatalf("name A: %v", err)
	}
	if _, err := app.EditSubscription(srvB.URL, srvB.URL, "Premium"); err != nil {
		t.Fatalf("name B: %v", err)
	}
	// A hand-picked node the user filed into a remark group of their own.
	if _, err := app.ImportLinks(nodeLink(5)); err != nil {
		t.Fatalf("import hand link: %v", err)
	}
	hand, ok := func() (Profile, bool) {
		for _, p := range store.ListProfiles() {
			if p.SubURL == "" {
				return p, true
			}
		}
		return Profile{}, false
	}()
	if !ok {
		t.Fatal("the hand-added profile never landed in the store")
	}
	hand.Group = "Premium"
	if _, err := store.SaveProfile(hand); err != nil {
		t.Fatal(err)
	}
	s := store.GetSettings()
	s.ProxyGroups = append(s.ProxyGroups, "Premium")
	if err := store.SaveSettings(s); err != nil {
		t.Fatal(err)
	}

	if _, err := app.EditSubscription(srvA.URL, srvA.URL, "Premium main"); err != nil {
		t.Fatalf("rename A: %v", err)
	}
	groups := groupsOf(store)
	if got := countInGroup(groups, "Premium main"); got != 2 {
		t.Fatalf("%d profiles follow the renamed feed, want its 2 nodes: %+v", got, groups)
	}
	if got := countInGroup(groups, "Premium"); got != 3 {
		t.Fatalf("%d profiles were dragged into the renamed feed's group, want the other feed's 2 plus the hand-added one: %+v", got, groups)
	}
	// The second feed keeps its own name; only its nodes move. Renaming it
	// must likewise leave the hand-added profile in the remark group it
	// shares with the old name.
	if _, err := app.EditSubscription(srvB.URL, srvB.URL, "Backup"); err != nil {
		t.Fatalf("rename B: %v", err)
	}
	groups = groupsOf(store)
	if got := countInGroup(groups, "Backup"); got != 2 {
		t.Fatalf("%d profiles under the second feed's new name, want 2: %+v", got, groups)
	}
	if groups[hand.Name] != "Premium" {
		t.Fatalf("hand-added profile %q joined group %q, want the manual group it was filed in", hand.Name, groups[hand.Name])
	}
}

// TestFeedRenameStillMovesLegacyProfiles: profiles imported before
// SubURL existed carry no feed pointer at all, so the old name is the
// only evidence they belong to the feed. Dropping that fallback would
// strand them in a group that vanishes on the next refresh — as long as
// no remark group of the user's shares the name, which is what the
// guard above checks.
func TestFeedRenameStillMovesLegacyProfiles(t *testing.T) {
	body := nodeLink(1) + "\n" + nodeLink(2)
	srv := subFeedServer(t, func() string { return body })
	store, err := NewConfigStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	app := NewApp(store, make(chan LogEntry, 256))
	if _, err := app.ImportLinks(srv.URL); err != nil {
		t.Fatalf("import: %v", err)
	}
	if _, err := app.EditSubscription(srv.URL, srv.URL, "My VPN"); err != nil {
		t.Fatalf("name: %v", err)
	}
	// Forget where each node came from, the way an old config file does.
	for _, p := range store.ListProfiles() {
		p.SubURL = ""
		if _, err := store.SaveProfile(p); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := app.EditSubscription(srv.URL, srv.URL, "My VPN 2"); err != nil {
		t.Fatalf("rename: %v", err)
	}
	for name, group := range groupsOf(store) {
		if group != "My VPN 2" {
			t.Fatalf("profile %q stayed in %q, want the renamed feed to carry it", name, group)
		}
	}
}

// visibleGroupsBody is the windowing computed, braces and all.
func visibleGroupsBody(t *testing.T) string {
	t.Helper()
	src := readFrontendSrc(t, "components/ProxiesTable.vue")
	re := regexp.MustCompile(`(?s)const visibleGroups = computed\(\(\) => \{.*?\n\}\)`)
	body := re.FindString(src)
	if body == "" {
		t.Fatal("ProxiesTable.vue no longer builds the rendered group list in visibleGroups")
	}
	return body
}

// fnBody returns one top-level `function name … { … }` block from src.
func fnBody(t *testing.T, src, name string) string {
	t.Helper()
	re := regexp.MustCompile(`(?s)function ` + regexp.QuoteMeta(name) + `\(.*?\n\}`)
	body := re.FindString(src)
	if body == "" {
		t.Fatalf("%s is gone from the frontend source", name)
	}
	return body
}

// TestGroupHeaderSelectsTheWholeGroup: rows select one at a time and the
// sticky bar acts on a selection, but a group — the unit a feed arrives
// in, and the thing the user reads the count of — had no select control.
// It has to count the group's members, not the rows currently in the DOM:
// a folded or not-yet-batched group would otherwise tick only its visible
// part while its pill claims more.
func TestGroupHeaderSelectsTheWholeGroup(t *testing.T) {
	src := readFrontendSrc(t, "components/ProxiesTable.vue")
	if !strings.Contains(src, `@change="toggleGroupSelection(g.name)"`) {
		t.Error("no group header control selects the whole group")
	}
	if !strings.Contains(src, `:indeterminate.prop="groupSelectionState(g.name) === 'some'"`) {
		t.Error("the group checkbox never shows the part-selected state its members can be in")
	}
	body := fnBody(t, src, "membersOfGroup")
	if !strings.Contains(body, "groupEntries") {
		t.Errorf("membersOfGroup does not read the full group list, so a folded group selects only what is on screen:\n%s", body)
	}
	if strings.Contains(body, "visibleGroups") {
		t.Errorf("membersOfGroup works off the windowed rows:\n%s", body)
	}
}

// TestEveryGroupKeepsItsHeader: the list renders in bounded batches, and
// the loop bailed out as soon as the row budget ran out — which threw
// away the groups further down, including folded ones and empty remark
// groups that contribute no rows at all. A collapsed feed past row 200
// therefore disappeared from the tab with nothing left to scroll to,
// since the "showing N of M" count only ever adds rows: the group looked
// deleted. Headers cost no rows, so only row output may wait for the
// next batch.
func TestEveryGroupKeepsItsHeader(t *testing.T) {
	body := visibleGroupsBody(t)
	if strings.Contains(body, "break") {
		t.Errorf("visibleGroups still leaves the group loop early, which drops the headers of the groups after it:\n%s", body)
	}
	if strings.Contains(body, "if (used >= limit) continue") {
		t.Fatal("row budget still skips empty and expanded group headers")
	}
	if !strings.Contains(body, "rows.slice(0, Math.max(0, limit - used))") {
		t.Fatal("row output must remain bounded while headers stay visible")
	}
}

// TestGroupDialogMovesTheWholeSelection: a group could only be filled one
// row at a time — the row menu and the Advanced profile tools each move a
// single profile — so sorting a few dozen nodes into a remark group meant
// a few dozen dialogs, and the sticky bar that already offers bulk
// delete/test/export had no bulk group action at all.
func TestGroupDialogMovesTheWholeSelection(t *testing.T) {
	src := readFrontendSrc(t, "components/ProxiesTable.vue")
	if !strings.Contains(src, "targets: Profile[]") {
		t.Error("groupDialog still carries a single profile, so the dialog cannot re-file a selection")
	}
	if !strings.Contains(src, `@click="openGroupDialogSelection"`) {
		t.Error("the sticky bar wires no bulk Move to group action")
	}
	// Once, from the row menu, and once from the sticky bar.
	if n := strings.Count(src, "Move to group"); n < 2 {
		t.Errorf("%d Move to group entry points, want the row menu plus the bulk one", n)
	}
	for _, want := range []string{"selectedIds.value", "profiles.save({ ...target"} {
		if !strings.Contains(src, want) {
			t.Errorf("bulk move never does %q", want)
		}
	}
}
