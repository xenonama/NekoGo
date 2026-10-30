package main

// proxyChain is the shape of one auxiliary-process chain link as the
// core managers consume it: a main-core inbound tag ("proxy"), the
// kind label of the linked process and the local port it listens on.
//
// The chain registry that used to fill it (spawned auxiliary processes
// publishing their chains) is gone, so nothing can publish a chain any
// more: chainsForTag always returns nil and the chain wrapping in
// singbox_manager.go / xray_manager.go is inert. Those two managers
// are protected from edits, so the accessor stays here for them.
type proxyChain struct {
	kind    string
	chainTo string
	port    int
}

// chainsForTag returns the active chain links for one main-core
// outbound tag. No publisher exists any more, so the list is always
// empty.
func chainsForTag(tag string) []proxyChain {
	return nil
}
