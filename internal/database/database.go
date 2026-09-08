package database

import (
	"crypto/rand"
	"fmt"
	"log"
	"strings"
	"time"

	"construct/source/internal/config"
	"construct/source/internal/models"

	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var DB *gorm.DB

func Init(cfg *config.Config) {
	var dialector gorm.Dialector

	switch cfg.DBDriver {
	case "postgres":
		dsn := fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=%s sslmode=disable",
			cfg.DBHost, cfg.DBPort, cfg.DBUser, cfg.DBPass, cfg.DBName)
		dialector = postgres.Open(dsn)
	default:
		dsn := fmt.Sprintf("%s:%s@tcp(%s:%s)/%s?charset=utf8mb4&parseTime=True&loc=Local",
			cfg.DBUser, cfg.DBPass, cfg.DBHost, cfg.DBPort, cfg.DBName)
		dialector = mysql.Open(dsn)
	}

	db, err := gorm.Open(dialector, &gorm.Config{
		Logger: logger.Default.LogMode(logger.Warn),
	})
	if err != nil {
		log.Fatalf("Failed to connect to database: %v", err)
	}

	DB = db
	if sqlDB, err := db.DB(); err == nil {
		sqlDB.SetMaxOpenConns(50)
		sqlDB.SetMaxIdleConns(10)
		sqlDB.SetConnMaxLifetime(30 * time.Minute)
	}

	// Backfill empty invite codes before migration adds unique index
	if db.Migrator().HasTable(&models.OrgInvite{}) {
		var emptyCodeInvites []models.OrgInvite
		db.Where("code = '' OR code IS NULL").Find(&emptyCodeInvites)
		for i := range emptyCodeInvites {
			emptyCodeInvites[i].Code = randomCode()
			db.Model(&emptyCodeInvites[i]).Update("code", emptyCodeInvites[i].Code)
		}
		if len(emptyCodeInvites) > 0 {
			log.Printf("[migrate] Backfilled %d invite codes", len(emptyCodeInvites))
		}
		// Drop old non-unique index if it exists so unique index can be created
		if db.Migrator().HasIndex(&models.OrgInvite{}, "idx_org_invites_code") {
			_ = db.Migrator().DropIndex(&models.OrgInvite{}, "idx_org_invites_code")
		}
	}

	err = db.AutoMigrate(
		&models.ProviderKey{},
		&models.Preference{},
		&models.OrgPreference{},
		&models.Organization{},
		&models.OrgMember{},
		&models.OrgRole{},
		&models.OrgRolePermission{},
		&models.Department{},
		&models.Team{},
		&models.TeamMember{},
		&models.OrgInvite{},
		&models.OrgActivity{},
		&models.OrgProviderKey{},
		&models.OrgSetting{},
		&models.OrgProject{},
		&models.OrgProjectMember{},
		&models.OrgProjectRepo{},
		&models.OrgSpace{},
		&models.OrgMemory{},
		// Provider catalog migrated to api/provider on 2026-05-19.
		// Source keeps ProviderKey (user BYOK) + OrgProviderKey (org
		// shared keys) only — both declared in user.go / org_resources.go.
		&models.FeedItem{},
		// Scheduler (universal cron primitive — see
		// construct-app/docs/plans/2026-05-20-automations.md)
		&models.ScheduledTask{},
		&models.ScheduledClaim{},
		&models.SchedulerDevice{},
	)
	if err != nil {
		log.Fatalf("Failed to migrate: %v", err)
	}

	// Seed built-in roles for orgs that don't have them yet
	seedBuiltinRoles(db)

	log.Printf("Connected to %s: %s@%s:%s", cfg.DBDriver, cfg.DBName, cfg.DBHost, cfg.DBPort)
}

// seedBuiltinRoles creates or updates built-in roles for all orgs.
// Creates missing roles, syncs permissions for existing built-in roles,
// and backfills role_id on members.
func seedBuiltinRoles(db *gorm.DB) {
	var orgs []models.Organization
	db.Find(&orgs)

	for _, org := range orgs {
		for _, def := range models.BuiltinRoles {
			var role models.OrgRole
			err := db.Where("org_id = ? AND name = ?", org.ID, def.Name).First(&role).Error

			if err != nil {
				// Create missing role
				role = models.OrgRole{
					ID:          randomUUID(),
					OrgID:       org.ID,
					Name:        def.Name,
					Description: def.Description,
					IsBuiltin:   true,
				}
				if err := db.Create(&role).Error; err != nil {
					log.Printf("[seed] Failed to create role %s: %v", def.Name, err)
					continue
				}
				log.Printf("[seed] Created role %s for org %s", def.Name, org.Name)
			}

			// Sync permissions — add any missing ones
			expanded := models.ExpandPermissions(def.Permissions)
			for _, perm := range expanded {
				var existing models.OrgRolePermission
				if db.Where("role_id = ? AND permission = ?", role.ID, perm).First(&existing).Error != nil {
					db.Create(&models.OrgRolePermission{
						ID:         randomUUID(),
						RoleID:     role.ID,
						Permission: perm,
					})
				}
			}
		}

		// Backfill role_id on members
		var members []models.OrgMember
		db.Where("org_id = ? AND role_id IS NULL", org.ID).Find(&members)
		for _, m := range members {
			roleName := strings.ToUpper(m.Role[:1]) + m.Role[1:]
			var role models.OrgRole
			if err := db.Where("org_id = ? AND name = ?", org.ID, roleName).First(&role).Error; err == nil {
				db.Model(&m).Update("role_id", role.ID)
			}
		}
	}
}

func randomUUID() string {
	b := make([]byte, 16)
	rand.Read(b)
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

func randomCode() string {
	const chars = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	b := make([]byte, 6)
	rand.Read(b)
	for i := range b {
		b[i] = chars[b[i]%byte(len(chars))]
	}
	return string(b)
}
