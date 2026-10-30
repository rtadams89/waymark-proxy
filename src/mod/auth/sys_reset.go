package auth

import (
	"fmt"

	"imuslab.com/zoraxy/mod/database"
)

// ResetAccount removes all admin accounts.
func ResetAccount(databaseBackend *string, databasePath *string) {
	fmt.Println("Resetting admin account...")

	backendType := database.GetRecommendedBackendType()
	if *databaseBackend != "auto" {
		switch *databaseBackend {
		case "leveldb":
			backendType = 2
		case "boltdb":
			backendType = 0
		case "fsdb":
			backendType = 1
		default:
			fmt.Println("Unknown database backend: " + *databaseBackend)
			fmt.Println("Using auto detected backend")
		}
	}

	db, err := database.NewDatabase(*databasePath, backendType)
	if err != nil {
		fmt.Println("Error opening database:", err)
		return
	}
	defer db.Close()

	if !db.TableExists("auth") {
		fmt.Println("No auth table found. Nothing to reset.")
		return
	}

	err = db.DropTable("auth")
	if err != nil {
		fmt.Println("Error dropping auth table:", err)
		return
	}

	fmt.Println("Successfully reset admin account. All users have been removed from the system.")
	fmt.Println("Please restart and create a new admin account.")
}
