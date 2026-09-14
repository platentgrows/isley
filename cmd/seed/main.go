// cmd/seed/main.go
//
// Scaled-back seed script for local development: a single grow space with
// 5 strains from 3 breeders, 6 plants spread across Seedling/Veg/Flower
// (since it's one space, plants are staggered rather than segregated by
// stage), a temperature + humidity sensor with 15 days of hourly data, and
// stage-appropriate activity history for each plant.
//
// All 6 plants share the same zone_id (there's only one space), so there's
// no zone-less/NULL-zone plant in this version at all — deliberately, to
// rule that class of bug out while debugging the larger script.
//
// Idempotent: gated by a single check (does the zone already exist?).
// Wipe the tables (or the SQLite file) to reseed from scratch.
//
// Run with: go run ./cmd/seed
package main

import (
	"database/sql"
	"fmt"
	"log"
	"math"
	"math/rand"
	"time"

	"isley/logger"
	"isley/model"
	"isley/utils"
)

var rng = rand.New(rand.NewSource(42))

func main() {
	logger.InitLogger()
	model.MigrateDB()
	model.InitDB()

	db, err := model.GetDB()
	if err != nil {
		log.Fatalf("failed to open db: %v", err)
	}
	defer db.Close()

	if alreadySeeded(db) {
		log.Println("fixture dataset already present (Grow Tent zone exists) — skipping. Wipe the DB to reseed.")
		return
	}

	zoneID := seedZone(db, "Grow Tent")
	breederIDs := seedBreeders(db)
	strains := seedStrains(db, breederIDs)
	activityIDs := seedActivities(db)
	seedSensorsAndData(db, zoneID)
	seedPlants(db, zoneID, strains, activityIDs)

	log.Println("seeding complete")
}

func alreadySeeded(db *sql.DB) bool {
	var exists bool
	if err := db.QueryRow(
		`SELECT EXISTS (SELECT 1 FROM zones WHERE name = $1)`, "Grow Tent",
	).Scan(&exists); err != nil {
		log.Fatalf("seed check failed: %v", err)
	}
	return exists
}

// ---------------------------------------------------------------------------
// Zone
// ---------------------------------------------------------------------------

func seedZone(db *sql.DB, name string) int64 {
	return upsertReturningID(db,
		`INSERT INTO zones (name)
		 SELECT $1 WHERE NOT EXISTS (SELECT 1 FROM zones WHERE name = $1)
		 RETURNING id`,
		`SELECT id FROM zones WHERE name = $1`,
		name, name,
	)
}

// ---------------------------------------------------------------------------
// Breeders + strains
// ---------------------------------------------------------------------------

var breederNames = []string{"Humboldt Seed Company", "DNA Genetics", "Barney's Farm"}

func seedBreeders(db *sql.DB) []int64 {
	ids := make([]int64, len(breederNames))
	for i, n := range breederNames {
		ids[i] = upsertReturningID(db,
			`INSERT INTO breeder (name)
			 SELECT $1 WHERE NOT EXISTS (SELECT 1 FROM breeder WHERE name = $1)
			 RETURNING id`,
			`SELECT id FROM breeder WHERE name = $1`,
			n, n,
		)
	}
	return ids
}

type seededStrain struct {
	id         int64
	name       string
	autoflower bool
	cycleTime  int // flowering length in days
}

var strainNames = []string{
	"Blue Dream", "OG Kush", "Gorilla Glue #4", "White Widow", "Wedding Cake",
}

var effectAdjectives = []string{"euphoric", "relaxing", "uplifting", "creative", "energizing"}
var flavorNotes = []string{"citrus", "diesel", "earthy", "berry", "gassy"}

func seedStrains(db *sql.DB, breederIDs []int64) []seededStrain {
	out := make([]seededStrain, 0, len(strainNames))
	for i, name := range strainNames {
		breederID := breederIDs[i%len(breederIDs)]
		autoflower := rng.Float64() < 0.3

		var sativa int
		switch rng.Intn(3) {
		case 0:
			sativa = 60 + rng.Intn(31)
		case 1:
			sativa = rng.Intn(31)
		default:
			sativa = 40 + rng.Intn(21)
		}
		indica := 100 - sativa
		cycleTime := 49 + rng.Intn(22)
		seedCount := []int{3, 5, 6, 10, 12}[rng.Intn(5)]

		dominant := "balanced hybrid"
		if sativa >= 60 {
			dominant = "sativa-dominant"
		} else if sativa <= 30 {
			dominant = "indica-dominant"
		}
		desc := fmt.Sprintf("%s %s with %s, %s flavor notes.",
			name, dominant,
			effectAdjectives[rng.Intn(len(effectAdjectives))],
			flavorNotes[rng.Intn(len(flavorNotes))],
		)

		autoInt := 0
		if autoflower {
			autoInt = 1
		}

		id := upsertReturningID(db,
			`INSERT INTO strain (name, sativa, indica, autoflower, description, seed_count, breeder_id)
			 SELECT $1, $2, $3, $4, $5, $6, $7
			 WHERE NOT EXISTS (SELECT 1 FROM strain WHERE name = $1)
			 RETURNING id`,
			`SELECT id FROM strain WHERE name = $1`,
			name,
			name, sativa, indica, autoInt, desc, seedCount, breederID,
		)

		out = append(out, seededStrain{id: id, name: name, autoflower: autoflower, cycleTime: cycleTime})
	}
	return out
}

// ---------------------------------------------------------------------------
// Activities
// ---------------------------------------------------------------------------

func seedActivities(db *sql.DB) map[string]int64 {
	ids := map[string]int64{}
	for _, n := range []string{"Water", "Feed", "Note"} {
		ids[n] = idForOnly(db, `SELECT id FROM activity WHERE name = $1`, n)
	}
	for _, n := range []string{"Transplant", "Topping", "Training (LST)"} {
		ids[n] = upsertReturningID(db,
			`INSERT INTO activity (name, lock)
			 SELECT $1, FALSE WHERE NOT EXISTS (SELECT 1 FROM activity WHERE name = $1)
			 RETURNING id`,
			`SELECT id FROM activity WHERE name = $1`,
			n, n,
		)
	}
	return ids
}

// ---------------------------------------------------------------------------
// Sensors + sensor_data
// ---------------------------------------------------------------------------

type sensorSpec struct {
	kind   string
	device string
	unit   string
	base   float64
	amp    float64
	noise  float64
}

func seedSensorsAndData(db *sql.DB, zoneID int64) {
	specs := []sensorSpec{
		{"temperature", "Govee H5075", "°F", 78, 3, 1},
		{"humidity", "Govee H5075", "%", 55, 5, 2},
	}

	now := time.Now()
	const hoursOfHistory = 15 * 24

	for _, s := range specs {
		label := fmt.Sprintf("Grow Tent %s", titleCase(s.kind))
		sensorID := upsertReturningID(db,
			`INSERT INTO sensors (name, zone_id, source, device, type, unit)
			 SELECT $1, $2, 'custom', $3, $4, $5
			 WHERE NOT EXISTS (SELECT 1 FROM sensors WHERE name = $1)
			 RETURNING id`,
			`SELECT id FROM sensors WHERE name = $1`,
			label,
			label, zoneID, s.device, s.kind, s.unit,
		)

		var already int
		if err := db.QueryRow(`SELECT COUNT(*) FROM sensor_data WHERE sensor_id = $1`, sensorID).Scan(&already); err != nil {
			log.Fatalf("seed sensor_data count check failed: %v", err)
		}
		if already > 0 {
			continue
		}

		tx, err := db.Begin()
		if err != nil {
			log.Fatalf("seed sensor_data begin tx failed: %v", err)
		}
		stmt, err := tx.Prepare(`INSERT INTO sensor_data (sensor_id, value, create_dt) VALUES ($1, $2, $3)`)
		if err != nil {
			log.Fatalf("seed sensor_data prepare failed: %v", err)
		}
		for h := hoursOfHistory; h >= 0; h-- {
			ts := now.Add(-time.Duration(h) * time.Hour)
			diurnal := math.Sin((float64(ts.Hour()) / 24) * 2 * math.Pi)
			val := s.base + diurnal*s.amp + (rng.Float64()*2-1)*s.noise
			// sensor_data.go stores/queries this column as UTC, formatted with
			// utils.LayoutDB — match that exactly rather than letting the
			// driver auto-convert a raw time.Time (which is what caused the
			// current_week NULL scans).
			if _, err := stmt.Exec(sensorID, val, ts.In(time.UTC).Format(utils.LayoutDB)); err != nil {
				log.Fatalf("seed sensor_data insert failed: %v", err)
			}
		}
		stmt.Close()
		if err := tx.Commit(); err != nil {
			log.Fatalf("seed sensor_data commit failed: %v", err)
		}
	}
}

func titleCase(s string) string {
	if s == "" {
		return s
	}
	return string(s[0]-32) + s[1:]
}

// ---------------------------------------------------------------------------
// Plants: growth-stage timeline (Germinating -> ... -> Flower; no
// Drying/Curing/Success here since this scaled-back version has no
// finished/harvested plants)
// ---------------------------------------------------------------------------

type stageOffset struct {
	status string
	day    int
}

func stageTimeline(autoflower bool) []stageOffset {
	flowerStart := 42
	if autoflower {
		flowerStart = 21
	}
	return []stageOffset{
		{"Germinating", 0},
		{"Planted", 3},
		{"Seedling", 7},
		{"Veg", 14},
		{"Flower", flowerStart},
	}
}

var notesByStage = map[string][]string{
	"Germinating": {"Taproot showing, moved to solo cup."},
	"Planted":     {"Cotyledons open, looking healthy."},
	"Seedling":    {"Second node set, growth looks vigorous.", "Looking healthy, no issues."},
	"Veg":         {"Vigorous growth, upped light intensity.", "Some yellowing on lower fan leaves, adjusted feed."},
	"Flower":      {"First pistils showing.", "Trichomes turning cloudy, checking daily now."},
}

var nameCounters = map[string]int{}

func nextPlantName(strainName string) string {
	nameCounters[strainName]++
	return fmt.Sprintf("%s #%d", strainName, nameCounters[strainName])
}

// seedPlants creates 6 plants in the single zone: 2 in Seedling, 2 in Veg,
// 2 in Flower, so the one space shows a realistic staggered mix.
func seedPlants(db *sql.DB, zoneID int64, strains []seededStrain, activityIDs map[string]int64) {
	now := time.Now()

	targetStages := []struct {
		status string
		minAge int
		maxAge int
	}{
		{"Seedling", 7, 13},
		{"Seedling", 7, 13},
		{"Veg", 14, 41},
		{"Veg", 14, 41},
		{"Flower", 42, 70},
		{"Flower", 42, 70},
	}

	for _, t := range targetStages {
		s := strains[rng.Intn(len(strains))]
		ageDays := t.minAge + rng.Intn(t.maxAge-t.minAge+1)
		germinationDate := now.AddDate(0, 0, -ageDays)
		seedOnePlant(db, activityIDs, s, zoneID, germinationDate)
	}
}

func seedOnePlant(db *sql.DB, activityIDs map[string]int64, s seededStrain, zoneID int64, germinationDate time.Time) {
	name := nextPlantName(s.name)
	now := time.Now()

	plantID := upsertReturningID(db,
		`INSERT INTO plant (name, description, clone, strain_id, zone_id, start_dt)
		 SELECT $1, '', 0, $2, $3, $4
		 WHERE NOT EXISTS (SELECT 1 FROM plant WHERE name = $1)
		 RETURNING id`,
		`SELECT id FROM plant WHERE name = $1`,
		name,
		// plant_status_update.go formats dates with utils.LayoutDateTimeLocal
		// before binding them — match that instead of passing a raw time.Time.
		name, s.id, zoneID, germinationDate.Format(utils.LayoutDateTimeLocal),
	)

	timeline := stageTimeline(s.autoflower)
	ageDays := int(now.Sub(germinationDate).Hours() / 24)

	for _, stage := range timeline {
		if stage.day > ageDays {
			break
		}
		statusID := idForOnly(db, `SELECT id FROM plant_status WHERE status = $1`, stage.status)
		logDate := germinationDate.AddDate(0, 0, stage.day)

		var exists bool
		if err := db.QueryRow(
			`SELECT EXISTS (SELECT 1 FROM plant_status_log WHERE plant_id = $1 AND status_id = $2)`,
			plantID, statusID,
		).Scan(&exists); err != nil {
			log.Fatalf("seed status_log exists check failed: %v", err)
		}
		if !exists {
			if _, err := db.Exec(
				`INSERT INTO plant_status_log (plant_id, status_id, date) VALUES ($1, $2, $3)`,
				plantID, statusID, logDate.Format(utils.LayoutDateTimeLocal),
			); err != nil {
				log.Fatalf("seed status_log insert failed: %v", err)
			}
		}
	}

	seedPlantActivityHistory(db, activityIDs, plantID, timeline, germinationDate, ageDays)
}

func seedPlantActivityHistory(db *sql.DB, activityIDs map[string]int64, plantID int64, timeline []stageOffset, germinationDate time.Time, endDay int) {
	var already int
	if err := db.QueryRow(`SELECT COUNT(*) FROM plant_activity WHERE plant_id = $1`, plantID).Scan(&already); err != nil {
		log.Fatalf("seed plant_activity count check failed: %v", err)
	}
	if already > 0 {
		return
	}

	stageAt := func(day int) string {
		current := timeline[0].status
		for _, s := range timeline {
			if s.day > day {
				break
			}
			current = s.status
		}
		return current
	}

	vegStartDay := -1
	for _, s := range timeline {
		if s.status == "Veg" {
			vegStartDay = s.day
		}
	}

	// One-time events.
	if 7 <= endDay {
		insertActivity(db, plantID, activityIDs["Transplant"], germinationDate.AddDate(0, 0, 7), "Transplanted seedling into solo cup.")
	}
	if vegStartDay >= 0 && vegStartDay <= endDay {
		insertActivity(db, plantID, activityIDs["Transplant"], germinationDate.AddDate(0, 0, vegStartDay), "Transplanted into final pot.")
		if rng.Float64() < 0.5 && vegStartDay+7 <= endDay {
			insertActivity(db, plantID, activityIDs["Topping"], germinationDate.AddDate(0, 0, vegStartDay+7), "Topped above the 4th node.")
		}
		if rng.Float64() < 0.5 && vegStartDay+10 <= endDay {
			insertActivity(db, plantID, activityIDs["Training (LST)"], germinationDate.AddDate(0, 0, vegStartDay+10), "Tied down main branches for even canopy coverage.")
		}
	}

	// Recurring watering/feeding/notes.
	for d := 2; d <= endDay; d++ {
		stage := stageAt(d)
		date := germinationDate.AddDate(0, 0, d)

		if d%3 == 0 {
			insertActivity(db, plantID, activityIDs["Water"], date, "")
		}
		if d%7 == 3 && (stage == "Veg" || stage == "Flower") {
			insertActivity(db, plantID, activityIDs["Feed"], date, "")
		}
		if pool, ok := notesByStage[stage]; ok && rng.Float64() < 0.1 {
			insertActivity(db, plantID, activityIDs["Note"], date, pool[rng.Intn(len(pool))])
		}
	}
}

func insertActivity(db *sql.DB, plantID, activityID int64, date time.Time, note string) {
	if _, err := db.Exec(
		`INSERT INTO plant_activity (plant_id, activity_id, date, note) VALUES ($1, $2, $3, $4)`,
		plantID, activityID, date.Format(utils.LayoutDateTimeLocal), note,
	); err != nil {
		log.Fatalf("seed plant_activity insert failed: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Shared helpers
// ---------------------------------------------------------------------------

func upsertReturningID(db *sql.DB, insertQuery, lookupQuery string, keyArg interface{}, insertArgs ...interface{}) int64 {
	var id int64
	err := db.QueryRow(insertQuery, insertArgs...).Scan(&id)
	if err == sql.ErrNoRows {
		if err := db.QueryRow(lookupQuery, keyArg).Scan(&id); err != nil {
			log.Fatalf("seed lookup failed: %v\nquery: %s", err, lookupQuery)
		}
		return id
	}
	if err != nil {
		log.Fatalf("seed insert failed: %v\nquery: %s", err, insertQuery)
	}
	return id
}

func idForOnly(db *sql.DB, query string, args ...interface{}) int64 {
	var id int64
	if err := db.QueryRow(query, args...).Scan(&id); err != nil {
		log.Fatalf("seed lookup failed: %v\nquery: %s", err, query)
	}
	return id
}
