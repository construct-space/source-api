package main

import (
	"context"
	"log"
	"net/http"
	"time"

	"construct/source/internal/config"
	"construct/source/internal/database"
	"construct/source/internal/devicebus"
	"construct/source/internal/handlers"
	"construct/source/internal/middleware"
	"construct/source/internal/scheduler"
)

func main() {
	cfg := config.Load()
	handlers.Cfg = cfg

	database.Init(cfg)

	// Sync builtin role permissions for all existing orgs
	handlers.SyncAllBuiltinRoles()

	// Device bus — in-process pub/sub for cross-device events
	// (assistant.ask, scheduler.claim_now, …). Migration target for
	// the relay traffic currently living in api/delivery (slice 4).
	handlers.Hub = devicebus.NewHub()

	// Scheduler — background reclaim of expired claims (devices that
	// crashed/disconnected mid-run). Long-running; tied to the
	// process lifetime via context cancellation on shutdown (none today,
	// but the wiring's there).
	scheduler.StartReclaimLoop(context.Background())

	// Scheduler — notifier: scan for due tasks every 5s, publish
	// scheduler.claim_now via the device bus so subscribed devices
	// claim + execute without waiting for the next poll.
	scheduler.StartNotifyLoop(context.Background(), handlers.Hub)

	// Provider catalog migrated to api/provider on 2026-05-19.
	// Source still owns user BYOK keys (/api/providers/keys/*) and org
	// shared keys (OrgProviderKey); the catalog itself is fetched from
	// provider-api by clients.

	mux := http.NewServeMux()
	auth := middleware.Auth(cfg)

	// Integration proxy — forwards /api/integration/* to integration-api
	// after upstream auth. Frontend hits this; never goes direct.
	{
		ip := auth(http.HandlerFunc(handlers.IntegrationProxy))
		for _, method := range []string{"GET", "POST", "PUT", "DELETE", "PATCH"} {
			mux.Handle(method+" /api/integration/{path...}", ip)
		}
	}

	// Providers — user BYOK key management. Catalog itself moved to
	// api/provider (clients fetch /api/providers from there directly).
	mux.Handle("GET /api/providers/keys", auth(http.HandlerFunc(handlers.ListKeys)))
	mux.Handle("PUT /api/providers/keys/{provider}", auth(http.HandlerFunc(handlers.SetKey)))
	mux.Handle("DELETE /api/providers/keys/{provider}", auth(http.HandlerFunc(handlers.DeleteKey)))

	// Org preferences — team-wide settings (Member Policies, project
	// defaults). User-scoped preferences moved to accounts.
	mux.Handle("GET /api/org/preferences", auth(http.HandlerFunc(handlers.GetOrgPreferences)))
	mux.Handle("PUT /api/org/preferences/{key}", auth(http.HandlerFunc(handlers.SetOrgPreference)))

	// Settings — system config (read-only)
	mux.Handle("GET /api/settings", auth(http.HandlerFunc(handlers.GetSettings)))

	// Org — CRUD
	mux.Handle("POST /api/org", auth(http.HandlerFunc(handlers.CreateOrg)))
	mux.Handle("GET /api/org", auth(http.HandlerFunc(handlers.GetOrg)))
	// Batch public-fields lookup for name-resolution UIs (install lists,
	// allowlists, transfers). Returns minimal {id, name, slug, icon}.
	mux.Handle("GET /api/orgs", auth(http.HandlerFunc(handlers.ListOrgsPublic)))
	mux.Handle("PUT /api/org", auth(http.HandlerFunc(handlers.UpdateOrg)))
	mux.Handle("DELETE /api/org", auth(http.HandlerFunc(handlers.DeleteOrg)))
	mux.Handle("GET /api/org/membership", auth(http.HandlerFunc(handlers.GetMembership)))
	mux.Handle("GET /api/org/managed-settings", auth(http.HandlerFunc(handlers.GetManagedSettings)))
	mux.Handle("GET /api/org/insights", auth(http.HandlerFunc(handlers.GetOrgInsights)))

	// Org — Members
	mux.Handle("GET /api/org/members", auth(http.HandlerFunc(handlers.ListMembers)))
	mux.Handle("GET /api/org/members/{id}", auth(http.HandlerFunc(handlers.GetMember)))
	mux.Handle("POST /api/org/members", auth(http.HandlerFunc(handlers.CreateMember)))
	mux.Handle("PUT /api/org/members/{id}", auth(http.HandlerFunc(handlers.UpdateMember)))
	mux.Handle("DELETE /api/org/members/{id}", auth(http.HandlerFunc(handlers.DeleteMember)))

	// Org — Departments
	mux.Handle("GET /api/org/departments", auth(http.HandlerFunc(handlers.ListDepartments)))
	mux.Handle("GET /api/org/departments/{id}", auth(http.HandlerFunc(handlers.GetDepartment)))
	mux.Handle("POST /api/org/departments", auth(http.HandlerFunc(handlers.CreateDepartment)))
	mux.Handle("PUT /api/org/departments/{id}", auth(http.HandlerFunc(handlers.UpdateDepartment)))
	mux.Handle("DELETE /api/org/departments/{id}", auth(http.HandlerFunc(handlers.DeleteDepartment)))

	// Org — Teams
	mux.Handle("GET /api/org/teams", auth(http.HandlerFunc(handlers.ListTeams)))
	mux.Handle("GET /api/org/teams/{id}", auth(http.HandlerFunc(handlers.GetTeam)))
	mux.Handle("POST /api/org/teams", auth(http.HandlerFunc(handlers.CreateTeam)))
	mux.Handle("PUT /api/org/teams/{id}", auth(http.HandlerFunc(handlers.UpdateTeam)))
	mux.Handle("DELETE /api/org/teams/{id}", auth(http.HandlerFunc(handlers.DeleteTeam)))
	mux.Handle("GET /api/org/teams/{id}/members", auth(http.HandlerFunc(handlers.ListTeamMembers)))
	mux.Handle("POST /api/org/teams/{id}/members", auth(http.HandlerFunc(handlers.AddTeamMember)))
	mux.Handle("DELETE /api/org/teams/{id}/members/{memberId}", auth(http.HandlerFunc(handlers.RemoveTeamMember)))

	// Org — Invites
	mux.Handle("GET /api/org/invites", auth(http.HandlerFunc(handlers.ListInvites)))
	mux.Handle("POST /api/org/invites", auth(http.HandlerFunc(handlers.CreateInvite)))
	mux.Handle("PUT /api/org/invites/{id}/revoke", auth(http.HandlerFunc(handlers.RevokeInvite)))
	mux.HandleFunc("GET /api/org/invites/{token}/info", handlers.GetInviteByToken) // no auth (read-only invite preview)
	// Accept REQUIRES auth: the joining user must be the authenticated caller,
	// not an attacker-supplied user_id. Wrapped so AcceptInvite can trust UserID(r).
	mux.Handle("POST /api/org/invites/{token}/accept", auth(http.HandlerFunc(handlers.AcceptInvite)))

	// Org — Roles & Permissions
	mux.Handle("GET /api/org/roles", auth(http.HandlerFunc(handlers.ListRoles)))
	mux.Handle("GET /api/org/roles/{id}", auth(http.HandlerFunc(handlers.GetRole)))
	mux.Handle("POST /api/org/roles", auth(http.HandlerFunc(handlers.CreateRole)))
	mux.Handle("PUT /api/org/roles/{id}", auth(http.HandlerFunc(handlers.UpdateRole)))
	mux.Handle("DELETE /api/org/roles/{id}", auth(http.HandlerFunc(handlers.DeleteRole)))
	mux.Handle("GET /api/org/permissions", auth(http.HandlerFunc(handlers.ListPermissions)))
	mux.Handle("PUT /api/org/members/{id}/role", auth(http.HandlerFunc(handlers.AssignMemberRole)))

	// Org — Activity
	mux.Handle("GET /api/org/activity", auth(http.HandlerFunc(handlers.ListActivity)))

	// Org — Spaces (admin-curated marketplace pins)
	mux.Handle("GET /api/org/spaces", auth(http.HandlerFunc(handlers.ListOrgSpaces)))
	mux.Handle("POST /api/org/spaces", auth(http.HandlerFunc(handlers.PinOrgSpace)))
	mux.Handle("DELETE /api/org/spaces/{spaceId}", auth(http.HandlerFunc(handlers.UnpinOrgSpace)))

	// Org — Provider Keys (org-level API keys)
	mux.Handle("GET /api/org/providers", auth(http.HandlerFunc(handlers.ListOrgProviders)))
	mux.Handle("GET /api/org/providers/{provider}/key", auth(http.HandlerFunc(handlers.GetOrgProviderKey)))
	mux.Handle("PUT /api/org/providers/{provider}", auth(http.HandlerFunc(handlers.SetOrgProvider)))
	mux.Handle("DELETE /api/org/providers/{provider}", auth(http.HandlerFunc(handlers.DeleteOrgProvider)))

	// Org — Memory (shared agent memory across the org's members)
	mux.Handle("GET /api/org/memory", auth(http.HandlerFunc(handlers.GetOrgMemory)))
	mux.Handle("PUT /api/org/memory", auth(http.HandlerFunc(handlers.PutOrgMemory)))

	// Org — Projects
	mux.Handle("GET /api/org/projects", auth(http.HandlerFunc(handlers.ListOrgProjects)))
	mux.Handle("GET /api/org/projects/{id}", auth(http.HandlerFunc(handlers.GetOrgProject)))
	mux.Handle("POST /api/org/projects", auth(http.HandlerFunc(handlers.CreateOrgProject)))
	mux.Handle("PUT /api/org/projects/{id}", auth(http.HandlerFunc(handlers.UpdateOrgProject)))
	mux.Handle("DELETE /api/org/projects/{id}", auth(http.HandlerFunc(handlers.DeleteOrgProject)))
	mux.Handle("GET /api/org/projects/{id}/members", auth(http.HandlerFunc(handlers.ListProjectMembers)))
	mux.Handle("POST /api/org/projects/{id}/members", auth(http.HandlerFunc(handlers.AddProjectMember)))
	mux.Handle("DELETE /api/org/projects/{id}/members/{memberId}", auth(http.HandlerFunc(handlers.RemoveProjectMember)))
	mux.Handle("GET /api/org/projects/{id}/repos", auth(http.HandlerFunc(handlers.ListProjectRepos)))
	mux.Handle("POST /api/org/projects/{id}/repos", auth(http.HandlerFunc(handlers.AddProjectRepo)))
	mux.Handle("DELETE /api/org/projects/{id}/repos/{repoId}", auth(http.HandlerFunc(handlers.RemoveProjectRepo)))

	// Org — Settings (org-level config)
	mux.Handle("GET /api/org/settings", auth(http.HandlerFunc(handlers.GetOrgSettings)))
	mux.Handle("PUT /api/org/settings/{key}", auth(http.HandlerFunc(handlers.SetOrgSetting)))
	mux.Handle("DELETE /api/org/settings/{key}", auth(http.HandlerFunc(handlers.DeleteOrgSetting)))

	// Feed — homepage announcements, tips, changelogs.
	// GET is public so the desktop app can populate its Home strip during
	// bootstrap (before login) and on accounts that haven't been granted
	// source scope. POST still requires auth — only admins seed content.
	mux.HandleFunc("GET /api/feed", handlers.GetFeed)
	mux.Handle("POST /api/feed", auth(http.HandlerFunc(handlers.PostFeedItem)))

	// Staff admin — feed CRUD via Oracle. Matches the AdminListOrgs /
	// AdminListProviders pattern: no auth wrapper, relying on /api/admin/*
	// being reachable only via oracle-api (which sends X-Internal-Secret)
	// and not exposed through the public gateway.
	mux.HandleFunc("GET /api/admin/feed-items", handlers.AdminListFeedItems)
	mux.HandleFunc("POST /api/admin/feed-items", handlers.AdminCreateFeedItem)
	mux.HandleFunc("PATCH /api/admin/feed-items/{id}", handlers.AdminUpdateFeedItem)
	mux.HandleFunc("DELETE /api/admin/feed-items/{id}", handlers.AdminDeleteFeedItem)
	mux.HandleFunc("POST /api/admin/feed-items/reorder", handlers.AdminReorderFeedItems)

	// Morpheus — verification pattern telemetry
	mux.Handle("POST /api/v1/morpheus/patterns", auth(http.HandlerFunc(handlers.SubmitVerificationPatterns)))

	// Internal service-to-service endpoints (X-Internal-Secret header)
	mux.HandleFunc("GET /internal/membership", handlers.InternalGetMembership)
	mux.HandleFunc("POST /internal/developer-role/seed", handlers.InternalSeedDeveloperRole)
	mux.HandleFunc("POST /internal/developer-role/unseed", handlers.InternalUnseedDeveloperRole)

	// Admin cross-org endpoints (X-Internal-Secret) — consumed by Oracle.
	mux.HandleFunc("GET /api/admin/orgs", handlers.AdminListOrgs)
	mux.HandleFunc("GET /api/admin/orgs/{id}", handlers.AdminGetOrg)
	mux.HandleFunc("GET /api/admin/orgs/{id}/members", handlers.AdminListOrgMembers)
	mux.HandleFunc("GET /api/admin/orgs/{id}/projects", handlers.AdminListOrgProjects)
	mux.HandleFunc("GET /api/admin/orgs/{id}/teams", handlers.AdminListOrgTeams)
	mux.HandleFunc("GET /api/admin/orgs/{id}/invites", handlers.AdminListOrgInvites)
	mux.HandleFunc("PUT /api/admin/org-invites/{id}/revoke", handlers.AdminRevokeInvite)

	// Admin provider catalog moved to api/provider on 2026-05-19.
	// Oracle proxies to provider-api now instead of going through source.

	// Scheduler — universal cron primitive used by Pulses, Calendar
	// reminders, Mail snooze, send-later, system jobs. Slice 1: CRUD +
	// device registration. Tick loop / claim / push wake land in
	// subsequent slices. See construct-app/docs/plans/2026-05-20-automations.md.
	mux.Handle("POST /api/scheduler/tasks", auth(http.HandlerFunc(handlers.CreateScheduledTask)))
	mux.Handle("GET /api/scheduler/tasks", auth(http.HandlerFunc(handlers.ListScheduledTasks)))
	mux.Handle("GET /api/scheduler/tasks/{id}", auth(http.HandlerFunc(handlers.GetScheduledTask)))
	mux.Handle("PATCH /api/scheduler/tasks/{id}", auth(http.HandlerFunc(handlers.UpdateScheduledTask)))
	mux.Handle("DELETE /api/scheduler/tasks/{id}", auth(http.HandlerFunc(handlers.DeleteScheduledTask)))
	mux.Handle("POST /api/scheduler/tasks/{id}/claim", auth(http.HandlerFunc(handlers.ClaimScheduledTask)))
	mux.Handle("POST /api/scheduler/tasks/{id}/report", auth(http.HandlerFunc(handlers.ReportScheduledTask)))
	mux.Handle("POST /api/scheduler/devices/register", auth(http.HandlerFunc(handlers.RegisterSchedulerDevice)))
	mux.Handle("POST /api/scheduler/devices/{id}/heartbeat", auth(http.HandlerFunc(handlers.HeartbeatSchedulerDevice)))

	// Device bus — WS subscribe (token via query for browser WS) +
	// REST relay for inter-device messages. See
	// internal/handlers/devicebus.go for envelope types in play.
	mux.Handle("GET /api/device-bus/ws", auth(http.HandlerFunc(handlers.WSDeviceBus)))
	mux.Handle("POST /api/device-bus/relay", auth(http.HandlerFunc(handlers.RelayPublish)))
	mux.Handle("GET /api/device-bus/operator/status", auth(http.HandlerFunc(handlers.OperatorStatus)))

	// Health
	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, r *http.Request) {
		handlers.WriteJSON(w, 200, map[string]any{"status": "ok"})
	})

	// Middleware stack
	var handler http.Handler = mux
	handler = middleware.CORS(cfg)(handler)
	handler = middleware.SecurityHeaders(handler)
	handler = middleware.Logger(handler)

	log.Printf("Construct Source running on :%s", cfg.Port)
	server := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	log.Fatal(server.ListenAndServe())
}
