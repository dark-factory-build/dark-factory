package maintainer

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/dark-factory-build/dark-factory/internal/install"
)

// Host owns the one private connection. Neither callers nor workers receive
// the broker credential. The installed home's lease owns the credential file.
type Host struct {
	mu         sync.Mutex
	home       *install.OperationalHome
	client     *Client
	connection connectionRecord
}
type connectionRecord struct {
	ID       string `json:"id"`
	Secret   string `json:"credential"`
	Disabled bool   `json:"disabled"`
}

func (connectionRecord) String() string   { return "MaintainerConnection(<redacted>)" }
func (connectionRecord) GoString() string { return "MaintainerConnection(<redacted>)" }
func (*Host) String() string              { return "MaintainerHost(<redacted>)" }
func (*Host) GoString() string            { return "MaintainerHost(<redacted>)" }

func OpenHost(home *install.OperationalHome) (*Host, error) {
	data, err := home.ReadMaintainerCredential()
	if err != nil {
		return nil, err
	}
	host := &Host{home: home, client: NewClient()}
	if len(data) > 0 {
		if json.Unmarshal(data, &host.connection) != nil {
			return nil, ErrInvalid
		}
		if host.connection.ID != "" {
			if _, err := parseCredential(host.connection.ID, host.connection.Secret); err != nil {
				return nil, err
			}
		}
	}
	return host, nil
}

func (host *Host) credential() (Credential, error) {
	if host.connection.Disabled {
		return Credential{}, ErrDenied
	}
	return parseCredential(host.connection.ID, host.connection.Secret)
}
func (host *Host) save(record connectionRecord) error {
	data, err := json.Marshal(record)
	if err != nil {
		return ErrInvalid
	}
	if err := host.home.WriteMaintainerCredential(data); err != nil {
		return err
	}
	host.connection = record
	return nil
}

func (host *Host) Connect(ctx context.Context) (Authorization, error) {
	// ponytail: one connection serializes remote calls. This intentionally
	// orders disconnect against publication; multiple connections need gates
	// per connection if the product supports them in a future release.
	host.mu.Lock()
	defer host.mu.Unlock()
	if host.connection.ID == "" && !host.connection.Disabled {
		lease, err := host.home.LockLegacyController()
		if err != nil {
			return Authorization{}, err
		}
		defer lease.Close()
	}
	if host.connection.ID != "" {
		if !host.connection.Disabled {
			return Authorization{}, ErrAlreadyConnected
		}
		credential, err := parseCredential(host.connection.ID, host.connection.Secret)
		if err != nil {
			return Authorization{}, err
		}
		if err := host.client.Disconnect(ctx, credential); err != nil {
			return Authorization{}, err
		}
	}
	credential, authorization, err := host.client.Connect(ctx)
	if err != nil {
		return Authorization{}, err
	}
	if err := host.save(connectionRecord{ID: credential.id, Secret: credential.secret}); err != nil {
		_ = host.client.Disconnect(ctx, credential)
		return Authorization{}, err
	}
	return authorization, nil
}
func (host *Host) Status(ctx context.Context) (Status, error) {
	host.mu.Lock()
	defer host.mu.Unlock()
	if host.connection.ID == "" {
		return Status{State: "disconnected", Repositories: []Delegation{}}, nil
	}
	if host.connection.Disabled {
		return Status{State: "disconnect_pending", Repositories: []Delegation{}}, nil
	}
	credential, err := host.credential()
	if err != nil {
		return Status{}, err
	}
	return host.client.Status(ctx, credential)
}

func (host *Host) Confirm(ctx context.Context, code string) error {
	host.mu.Lock()
	defer host.mu.Unlock()
	credential, err := host.credential()
	if err != nil {
		return err
	}
	return host.client.Confirm(ctx, credential, code)
}
func (host *Host) Installations(ctx context.Context, page int) (Installations, error) {
	host.mu.Lock()
	defer host.mu.Unlock()
	credential, err := host.credential()
	if err != nil {
		return Installations{}, err
	}
	return host.client.Installations(ctx, credential, page)
}
func (host *Host) Repositories(ctx context.Context, installation int64, page int) (Repositories, error) {
	host.mu.Lock()
	defer host.mu.Unlock()
	credential, err := host.credential()
	if err != nil {
		return Repositories{}, err
	}
	return host.client.Repositories(ctx, credential, installation, page)
}
func (host *Host) Delegate(ctx context.Context, repositories []Delegation) error {
	host.mu.Lock()
	defer host.mu.Unlock()
	credential, err := host.credential()
	if err != nil {
		return err
	}
	return host.client.Delegate(ctx, credential, repositories)
}
func (host *Host) Disconnect(ctx context.Context) error {
	host.mu.Lock()
	defer host.mu.Unlock()
	if host.connection.ID == "" {
		return nil
	}
	credential, err := parseCredential(host.connection.ID, host.connection.Secret)
	if err != nil {
		return err
	}
	record := host.connection
	record.Disabled = true
	if err := host.save(record); err != nil {
		return err
	}
	if err := host.client.Disconnect(ctx, credential); err != nil {
		return err
	}
	return host.save(connectionRecord{Disabled: true})
}
