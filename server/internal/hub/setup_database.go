package hub

import (
	"context"
	"errors"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/pgdocker"
)

// --- database ---------------------------------------------------------------------------------------------------

func (e *setupEnv) database(ctx context.Context, cfg *setupConfig) error {
	if cfg.DatabaseURL != "" {
		err := e.pingDB(ctx, cfg.DatabaseURL)
		if err != nil && e.containerExists(ctx) {
			// The container we made may just be stopped (Docker Desktop not started at login, say).
			if e.startContainer(ctx, cfg.DatabaseURL) == nil {
				err = e.pingDB(ctx, cfg.DatabaseURL)
			}
		}
		if err != nil {
			return e.fail("database", i18n.T(e.l, "hub-setup.db_unreachable", i18n.Args{"error": err.Error()}), i18n.T(e.l, "hub-setup.db_unreachable_next"))
		}
		e.report("database", true, i18n.T(e.l, "hub-setup.db_configured"), "")
		return nil
	}
	switch err := pgdocker.CheckDocker(ctx, e.run, e.lookPath); {
	case errors.Is(err, pgdocker.ErrNoDocker):
		return e.noDocker()
	case errors.Is(err, pgdocker.ErrDockerDenied):
		return e.fail("database", i18n.T(e.l, "hub-setup.docker_denied"), i18n.T(e.l, "hub-setup.docker_denied_next", i18n.Args{"user": e.user}))
	case err != nil:
		return e.fail("database", i18n.T(e.l, "hub-setup.docker_stopped"), e.startDockerHint())
	}
	pg := e.pg()
	created := false
	var password string
	if !pg.Exists(ctx) {
		password = randomHex(24)
		var step *pgdocker.StepError
		if err := pg.Create(ctx, password); errors.As(err, &step) && step.Step == "volume" {
			return e.fail("database", i18n.T(e.l, "hub-setup.db_volume_failed", i18n.Args{"volume": e.dbVolume, "error": step.Output}),
				i18n.T(e.l, "hub-setup.db_volume_failed_next"))
		} else if err != nil {
			why := err.Error()
			if step != nil {
				why = step.Output
			}
			return e.fail("database", i18n.T(e.l, "hub-setup.db_container_failed", i18n.Args{"container": e.dbContainer, "error": why}),
				i18n.T(e.l, "hub-setup.db_container_failed_next", i18n.Args{"port": e.dbPort, "image": pgdocker.Image}))
		}
		created = true
	} else {
		// The container is there but hub.json lost its address: the password it was made with is in its environment.
		var err error
		if password, err = pg.Password(ctx); err != nil {
			return e.fail("database", i18n.T(e.l, "hub-setup.db_password_unknown", i18n.Args{"container": e.dbContainer}),
				i18n.T(e.l, "hub-setup.db_password_unknown_next", i18n.Args{"port": e.dbPort}))
		}
	}
	if err := e.startContainer(ctx, ""); err != nil {
		return e.fail("database", i18n.T(e.l, "hub-setup.db_container_not_ready", i18n.Args{"container": e.dbContainer, "error": err.Error()}),
			i18n.T(e.l, "hub-setup.docker_logs_next", i18n.Args{"container": e.dbContainer}))
	}
	port, err := pg.HostPort(ctx)
	if err != nil {
		port = e.dbPort // a container on the host's network (db.sh): its port is the one it was made with
	}
	dbURL := pgdocker.URL(password, port)
	if err := editHubJSON(e.root, func(o *jsonObject) error { return o.Set("database_url", dbURL) }); err != nil {
		return e.fail("database", e.writeFailed(e.hubJSON(), err), i18n.T(e.l, "hub-setup.check_permissions"))
	}
	cfg.DatabaseURL = dbURL
	if err := e.pingUntil(ctx, dbURL); err != nil {
		return e.fail("database", i18n.T(e.l, "hub-setup.db_container_unreachable", i18n.Args{"error": err.Error()}),
			i18n.T(e.l, "hub-setup.docker_logs_next", i18n.Args{"container": e.dbContainer}))
	}
	if created {
		e.report("database", true, i18n.T(e.l, "hub-setup.db_created", i18n.Args{"container": e.dbContainer, "volume": e.dbVolume}), "")
	} else {
		e.report("database", true, i18n.T(e.l, "hub-setup.db_existing", i18n.Args{"container": e.dbContainer}), "")
	}
	return nil
}

// pingUntil connects to the database, again for a while: the port Docker publishes may lag the server inside.
func (e *setupEnv) pingUntil(ctx context.Context, dbURL string) error {
	deadline := time.Now().Add(e.dbWait)
	for {
		err := e.pingDB(ctx, dbURL)
		if err == nil || !time.Now().Before(deadline) || ctx.Err() != nil {
			return err
		}
		e.sleep(time.Second)
	}
}

func (e *setupEnv) noDocker() error {
	if !e.portFree(5432) {
		return e.fail("database", i18n.T(e.l, "hub-setup.no_docker_postgres"),
			i18n.T(e.l, "hub-setup.no_docker_postgres_next", i18n.Args{"path": e.hubJSON()}))
	}
	if e.goos == "darwin" {
		return e.fail("database", i18n.T(e.l, "hub-setup.no_database"), i18n.T(e.l, "hub-setup.no_database_next_mac"))
	}
	return e.fail("database", i18n.T(e.l, "hub-setup.no_database"), i18n.T(e.l, "hub-setup.no_database_next_linux"))
}

func (e *setupEnv) startDockerHint() string {
	if e.goos == "darwin" {
		return i18n.T(e.l, "hub-setup.docker_start_mac")
	}
	return i18n.T(e.l, "hub-setup.docker_start_linux")
}

// pg is the database container setup makes (persona-postgres unless a trial run named another).
func (e *setupEnv) pg() *pgdocker.Container {
	return &pgdocker.Container{Name: e.dbContainer, Volume: e.dbVolume, Port: e.dbPort, Run: e.run, Sleep: e.sleep, Wait: e.dbWait}
}

func (e *setupEnv) containerExists(ctx context.Context) bool { return e.pg().Exists(ctx) }

// startContainer starts the container and waits until Postgres in it answers (pgdocker.Container.Start).
func (e *setupEnv) startContainer(ctx context.Context, dbURL string) error {
	return e.pg().Start(ctx, dbURL)
}
