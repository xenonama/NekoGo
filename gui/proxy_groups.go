package main

import (
	"fmt"
	"slices"
	"strings"
)

func (a *App) RenameProxyGroup(oldName, newName string) error {
	if err := a.store.RenameProxyGroup(oldName, newName); err != nil {
		return err
	}
	a.refreshTray()
	return nil
}

func (a *App) RemoveProxyGroup(name string) error {
	if err := a.store.RemoveProxyGroup(name); err != nil {
		return err
	}
	a.refreshTray()
	return nil
}

func (c *ConfigStore) RenameProxyGroup(oldName, newName string) error {
	oldName, newName = strings.TrimSpace(oldName), strings.TrimSpace(newName)
	if oldName == "" || newName == "" {
		return fmt.Errorf("old and new proxy group names are required")
	}
	return c.editProxyGroup(oldName, newName)
}

func (c *ConfigStore) RemoveProxyGroup(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("proxy group name is required")
	}
	return c.editProxyGroup(name, "")
}

func (c *ConfigStore) editProxyGroup(oldName, newName string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.proxyGroupExistsLocked(oldName) {
		return fmt.Errorf("proxy group %q not found", oldName)
	}
	if oldName == newName {
		return nil
	}
	if newName != "" && c.proxyGroupExistsLocked(newName) {
		return fmt.Errorf("proxy group %q already exists", newName)
	}

	profiles := c.profiles
	groups := c.settings.ProxyGroups
	collapsed := c.settings.CollapsedProxyGroups
	// Only Group is mutated: a shallow profile copy isolates the rollback state.
	c.profiles = cloneSlice(profiles)
	for i := range c.profiles {
		if c.profiles[i].Group == oldName {
			c.profiles[i].Group = newName
		}
	}
	c.settings.ProxyGroups = editedProxyGroupNames(groups, oldName, newName)
	if newName != "" && !slices.Contains(c.settings.ProxyGroups, newName) {
		c.settings.ProxyGroups = append(c.settings.ProxyGroups, newName)
	}
	c.settings.CollapsedProxyGroups = editedProxyGroupNames(collapsed, oldName, newName)

	if err := c.saveLocked(); err != nil {
		c.profiles = profiles
		c.settings.ProxyGroups = groups
		c.settings.CollapsedProxyGroups = collapsed
		return err
	}
	return nil
}

func (c *ConfigStore) proxyGroupExistsLocked(name string) bool {
	if slices.Contains(c.settings.ProxyGroups, name) {
		return true
	}
	for _, p := range c.profiles {
		if p.Group == name {
			return true
		}
	}
	return false
}

func editedProxyGroupNames(names []string, oldName, newName string) []string {
	out := cloneSlice(names)
	if newName == "" {
		return slices.DeleteFunc(out, func(name string) bool { return name == oldName })
	}
	for i := range out {
		if out[i] == oldName {
			out[i] = newName
		}
	}
	return out
}
