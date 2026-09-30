//go:build esa

package main

import (
	"log"
	"sync"

	"silphuu/plugins/esa"
)

// The built-in outlet for an Alibaba Cloud ESA edge.
//
// Compiled only when the build asks for it (-tags esa), so a build that does not run ESA carries
// none of its vocabulary. Credentials come from ALIBABA_CLOUD_ACCESS_KEY_ID,
// ALIBABA_CLOUD_ACCESS_KEY_SECRET and ESA_SITE_ID.
//
// A node missing any of them has no outlet rather than a broken one: the flush is skipped and
// the ledger is left alone, the same shape as a command outlet with no command. The client is
// built once, on the first flush, so a node without credentials never reads them at startup.
func init() { setPurgeOutlet(&esaOutlet{}) }

type esaOutlet struct {
	once   sync.Once
	client *esa.Client
}

// Ready reports whether this node carries ESA credentials.
func (o *esaOutlet) Ready() bool {
	o.once.Do(func() {
		client, err := esa.NewClientFromEnv()
		if err != nil {
			log.Printf("purge: no ESA outlet on this node: %v", err)
			return
		}
		o.client = client
	})
	return o.client != nil
}

// Purge sends the URLs to ESA, which batches them at the API's own limit.
func (o *esaOutlet) Purge(urls []string) (sent bool, err error) {
	if !o.Ready() {
		return false, nil
	}
	resp, err := o.client.PurgeCaches(urls)
	if err != nil {
		// The URL list goes into the failure line too. The edge rejects the request as a
		// whole, so the only way to tell which URL it objected to is to have the list in
		// front of you; without it the failure is a dead end.
		log.Printf("purge: ESA refused %d URL(s): %v (URLs: %v)", len(urls), err, urls)
		return false, err
	}
	log.Printf("purge: ESA accepted %d URL(s), request %s", len(urls), resp.RequestID)
	return true, nil
}
