package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/samuelncui/yatm/config"
	"github.com/samuelncui/yatm/internal/buildinfo"
	legacy "github.com/samuelncui/yatm/migrate/legacy"
)

var (
	configPath     = flag.String("config", "./config.yaml", "config file path")
	phase          = flag.String("phase", "prepare", "schema, preflight, quiesce, frontend-check, prepare, commit, abort, cleanup, or repair-job")
	jobID          = flag.Int64("job-id", 0, "Job ID to repair after a committed migration")
	confirm        = flag.Bool("confirm", false, "confirm a destructive commit, abort, cleanup, or repair phase")
	serviceStopped = flag.Bool("service-stopped", false, "confirm the YATM service is already stopped during preflight")
	installRoot    = flag.String("install-root", "", "validate complete automatic-upgrade backup coverage under this directory")
	jsonOutput     = flag.Bool("json", false, "print the preflight report as JSON")
	showVersion    = flag.Bool("version", false, "print the local program version and exit")
)

func main() {
	// Load the offline migration configuration and database.
	flag.Parse()
	if *showVersion {
		if err := buildinfo.Write(os.Stdout, "yatm-migrate"); err != nil {
			log.Fatal(err)
		}
		return
	}
	if *phase == "commit" || *phase == "abort" || *phase == "cleanup" || *phase == "repair-job" || *phase == "quiesce" {
		requireConfirmation()
	}
	conf, err := config.Load(*configPath)
	if err != nil {
		log.Fatal(err)
	}
	if *phase == "frontend-check" {
		if err := checkFrontend(context.Background(), conf, "./frontend"); err != nil {
			log.Fatal(err)
		}
		return
	}
	if *installRoot != "" && conf.Database.Dialect != "sqlite" {
		log.Fatal("automatic upgrade requires a complete local SQLite installation; use the manual migration guide")
	}
	db, err := openMigrationDB(conf, *phase == "schema" || *phase == "preflight" || *phase == "quiesce")
	if err != nil {
		log.Fatal(err)
	}
	if db != nil {
		sqlDB, _ := db.DB()
		defer sqlDB.Close()
	}

	// Run exactly one requested migration phase.
	switch *phase {
	case "schema":
		schema, err := installedSchema(db)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Println(schema)
	case "preflight":
		report, err := inspectInstallation(context.Background(), db, conf, *configPath, *installRoot, *serviceStopped)
		if report != nil {
			encoder := json.NewEncoder(os.Stdout)
			if !*jsonOutput {
				encoder.SetIndent("", "  ")
			}
			if encodeErr := encoder.Encode(report); encodeErr != nil {
				log.Fatal(encodeErr)
			}
		}
		if err != nil {
			log.Fatal(err)
		}
	case "quiesce":
		if _, err := inspectInstallation(context.Background(), db, conf, *configPath, *installRoot, *serviceStopped); err != nil {
			log.Fatal(err)
		}
		schema, _ := installedSchema(db)
		if schema == legacy.SchemaCurrent && !*serviceStopped {
			ids, err := quiesceService(context.Background(), conf.Listen)
			if err != nil {
				log.Fatal(err)
			}
			if len(ids) != 0 {
				log.Fatalf("running Jobs must finish before upgrade, ids=%v", ids)
			}
		}
	case "prepare":
		indexRoot := legacy.LegacyLTFSIndexRoot(conf.Scripts.Mount, conf.Paths.Work)
		report, err := legacy.PrepareWithLTFSIndex(context.Background(), db, conf.Paths.Work, indexRoot, conf.Paths.Target)
		if report != nil {
			data, _ := json.MarshalIndent(report, "", "  ")
			fmt.Println(string(data))
		}
		if err != nil {
			log.Fatal(err)
		}
	case "commit":
		if err := legacy.Commit(context.Background(), db, conf.Paths.Work); err != nil {
			log.Fatal(err)
		}
	case "cleanup":
		if err := legacy.Cleanup(db, conf.Paths.Work); err != nil {
			log.Fatal(err)
		}
	case "abort":
		if err := legacy.Abort(db, conf.Paths.Work); err != nil {
			log.Fatal(err)
		}
	case "repair-job":
		if err := legacy.RepairJob(context.Background(), db, conf.Paths.Work, *jobID, conf.Paths.Target); err != nil {
			log.Fatal(err)
		}
	default:
		log.Fatalf("unknown migration phase %q", *phase)
	}
}

func requireConfirmation() {
	if !*confirm {
		log.Fatalf("phase %q requires --confirm", *phase)
	}
}
