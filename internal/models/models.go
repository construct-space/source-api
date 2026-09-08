// Package models defines all database models for Construct Source.
//
// Split by domain:
//   - user.go         — ProviderKey, Preference
//   - org.go          — Organization, OrgMember
//   - org_roles.go    — OrgRole, OrgRolePermission, permission definitions
//   - org_structure.go — Department, Team, TeamMember, OrgInvite
//   - org_resources.go — OrgActivity, OrgProviderKey, OrgSetting, OrgProject
package models
