package core

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/dynesshely/bot-harness/internal/qq"
)

// identityTTL is how long a group's bot_state is trusted before re-fetching.
// Kept short so a permission change made in the QQ group shows up in the admin
// panel quickly; a background refresher additionally re-reads every known group
// on an interval, and the admin panel can force it.
const identityTTL = 3 * time.Minute

// groupIdentity caches our own state inside one group.
type groupIdentity struct {
	state     *qq.BotState
	fetchedAt time.Time
	lastErr   string
}

// Identity answers "is this us?" for events coming out of a group.
//
// This matters as soon as a group contains more than one bot: an @ that targets
// the *other* bot also carries `mentions: [{bot: true, ...}]`, so counting bot
// mentions would make us answer messages addressed to somebody else. The only
// reliable discriminator is our own member_openid inside that group, which the
// platform exposes through /v2/groups/{group_openid}/bot_state.
type Identity struct {
	client *qq.Client
	// fallbackOpenID is QQ_BOT_OPENID, used when bot_state is unavailable
	// (the endpoint is whitelist-gated for some apps).
	fallbackOpenID string
	log            *slog.Logger

	onChange func(groupOpenID, previousSetting string, state *qq.BotState)

	mu          sync.Mutex
	groups      map[string]*groupIdentity
	loading     map[string]*sync.WaitGroup
	lastSetting map[string]string
	me          *qq.BotInfo
	meErr       string
}

// NewIdentity builds the identity resolver.
//
// onChange (set by the dispatcher) fires when a group's receive setting changes;
// the platform pushes no event for that, so it is how the panel stays truthful.
func NewIdentity(client *qq.Client, fallbackOpenID string, logger *slog.Logger) *Identity {
	if logger == nil {
		logger = slog.Default()
	}
	id := &Identity{
		client:         client,
		fallbackOpenID: fallbackOpenID,
		log:            logger,
		groups:         map[string]*groupIdentity{},
		loading:        map[string]*sync.WaitGroup{},
		lastSetting:    map[string]string{},
	}
	return id
}

// KnownGroups lists the groups whose state has been fetched.
func (id *Identity) KnownGroups() []string {
	id.mu.Lock()
	defer id.mu.Unlock()
	out := make([]string, 0, len(id.groups))
	for groupOpenID, cached := range id.groups {
		if cached.state != nil {
			out = append(out, groupOpenID)
		}
	}
	return out
}

// Me returns our own bot identity, fetching it once.
func (id *Identity) Me(ctx context.Context) *qq.BotInfo {
	id.mu.Lock()
	if id.me != nil {
		me := id.me
		id.mu.Unlock()
		return me
	}
	id.mu.Unlock()

	me, err := id.client.GetMe(ctx)
	id.mu.Lock()
	defer id.mu.Unlock()
	if err != nil {
		if id.meErr == "" {
			id.meErr = err.Error()
			id.log.Warn("failed to fetch the bot identity (/users/@me); "+
				"telling our own messages from another bot's will rely on other signals",
				"component", "identity", "error", err.Error())
		}
		return nil
	}
	id.me = me
	id.meErr = ""
	return me
}

// GroupState returns our cached state in a group, refreshing it when stale.
// The second return value is false when the state is unavailable.
func (id *Identity) GroupState(ctx context.Context, groupOpenID string) (*qq.BotState, bool) {
	return id.groupState(ctx, groupOpenID, identityTTL, false)
}

// Refresh re-reads a group's state regardless of the cache.
func (id *Identity) Refresh(ctx context.Context, groupOpenID string) (*qq.BotState, bool) {
	state, _ := id.groupState(ctx, groupOpenID, 0, true)
	return state, state != nil
}

// GroupStateFresh reads a group's state if the cache is older than maxAge.
func (id *Identity) GroupStateFresh(ctx context.Context, groupOpenID string, maxAge time.Duration) (*qq.BotState, bool) {
	return id.groupState(ctx, groupOpenID, maxAge, false)
}

func (id *Identity) groupState(ctx context.Context, groupOpenID string, maxAge time.Duration, force bool) (*qq.BotState, bool) {
	if groupOpenID == "" {
		return nil, false
	}

	id.mu.Lock()
	if cached, ok := id.groups[groupOpenID]; ok && !force && time.Since(cached.fetchedAt) < maxAge {
		state := cached.state
		id.mu.Unlock()
		return state, state != nil
	}
	// Single-flight: only one goroutine fetches a given group at a time.
	if wg, ok := id.loading[groupOpenID]; ok {
		id.mu.Unlock()
		wg.Wait()
		id.mu.Lock()
		cached := id.groups[groupOpenID]
		id.mu.Unlock()
		if cached == nil {
			return nil, false
		}
		return cached.state, cached.state != nil
	}
	wg := &sync.WaitGroup{}
	wg.Add(1)
	id.loading[groupOpenID] = wg
	id.mu.Unlock()

	state, err := id.client.GetBotState(ctx, groupOpenID)

	id.mu.Lock()
	if err != nil {
		previous := ""
		if old, ok := id.groups[groupOpenID]; ok && old.lastErr != "" {
			previous = old.lastErr
		}
		id.groups[groupOpenID] = &groupIdentity{fetchedAt: time.Now(), lastErr: err.Error()}
		if previous != err.Error() {
			id.log.Warn("failed to fetch the bot state for a group; "+
				"@ detection will fall back to the configured QQ_BOT_OPENID or the bot username",
				"component", "identity", "group_openid", groupOpenID, "error", err.Error())
		}
	} else {
		id.groups[groupOpenID] = &groupIdentity{state: state, fetchedAt: time.Now()}
	}
	delete(id.loading, groupOpenID)
	previous := id.lastSetting[groupOpenID]
	if state != nil {
		id.lastSetting[groupOpenID] = state.RecvMsgSetting
	}
	onChange := id.onChange
	id.mu.Unlock()
	wg.Done()

	if state != nil && previous != state.RecvMsgSetting && onChange != nil {
		onChange(groupOpenID, previous, state)
	}
	return state, err == nil && state != nil
}

// SelfMemberOpenID returns our own openid inside the group, or "" when unknown.
func (id *Identity) SelfMemberOpenID(ctx context.Context, groupOpenID string) string {
	if state, ok := id.GroupState(ctx, groupOpenID); ok && state.MemberOpenID != "" {
		return state.MemberOpenID
	}
	return id.fallbackOpenID
}

// MatchesMention reports whether a mention entry refers to this bot.
func (id *Identity) MatchesMention(ctx context.Context, groupOpenID string, m qq.User) bool {
	if !m.Bot {
		// A human mention is never us; being conservative here is what keeps us
		// quiet when somebody @s a person.
		return false
	}

	self := id.SelfMemberOpenID(ctx, groupOpenID)
	if self != "" {
		return m.MemberOpenID == self || m.ID == self
	}

	// bot_state unavailable: fall back to the bot's own username.
	if me := id.Me(ctx); me != nil && me.Username != "" {
		return m.Username == me.Username
	}

	// We cannot tell whose bot was mentioned. Staying silent is the lesser
	// evil: answering another bot's mentions is what users notice first.
	return false
}

// IsSelfAuthor reports whether a message was authored by this bot.
func (id *Identity) IsSelfAuthor(ctx context.Context, groupOpenID string, author qq.User) bool {
	if !author.Bot {
		return false
	}
	self := id.SelfMemberOpenID(ctx, groupOpenID)
	if self != "" {
		return author.MemberOpenID == self || author.ID == self
	}
	if me := id.Me(ctx); me != nil && me.Username != "" {
		return author.Username == me.Username
	}
	return false
}

// Snapshot is the admin-visible view of the identity resolver.
type IdentitySnapshot struct {
	BotID       string `json:"bot_id"`
	BotUsername string `json:"bot_username"`
	MeError     string `json:"me_error"`
	FallbackSet bool   `json:"fallback_openid_configured"`
}

// Snapshot reports the resolved own-identity for the admin panel.
func (id *Identity) Snapshot() IdentitySnapshot {
	id.mu.Lock()
	defer id.mu.Unlock()
	out := IdentitySnapshot{FallbackSet: id.fallbackOpenID != "", MeError: id.meErr}
	if id.me != nil {
		out.BotID = id.me.ID
		out.BotUsername = id.me.Username
	}
	return out
}

// GroupSnapshot is the admin-visible view of one group's platform state.
type GroupSnapshot struct {
	MemberOpenID      string `json:"member_openid"`
	RecvMsgSetting    string `json:"recv_msg_setting"`
	AllowProactiveMsg bool   `json:"allow_proactive_msg"`
	MemberRole        string `json:"member_role"`
	JoinedAt          string `json:"joined_at"`
	FetchedAt         string `json:"fetched_at"`
	Error             string `json:"error,omitempty"`
}

// GroupSnapshot reports the cached platform state of a group, if any.
func (id *Identity) GroupSnapshot(groupOpenID string) *GroupSnapshot {
	id.mu.Lock()
	defer id.mu.Unlock()
	cached, ok := id.groups[groupOpenID]
	if !ok {
		return nil
	}
	out := &GroupSnapshot{FetchedAt: cached.fetchedAt.Format(time.RFC3339), Error: cached.lastErr}
	if cached.state != nil {
		out.MemberOpenID = cached.state.MemberOpenID
		out.RecvMsgSetting = cached.state.RecvMsgSetting
		out.AllowProactiveMsg = cached.state.AllowProactiveMsg
		out.MemberRole = cached.state.MemberRole
		out.JoinedAt = cached.state.JoinedAt
	}
	return out
}
