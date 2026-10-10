# Database setup and migration

lalmax-nvr stores device configuration, recording indexes, events, VoIP call history, and other application state in its database. Recording media remains in the configured storage directory. SQLite is the default and requires no separate service. PostgreSQL and MySQL can also run as the active database.

## Configure a database

For a new deployment, leave `storage.database_driver` and `storage.database_dsn` unset to use `{storage.root_dir}/lalmax-nvr.db` with SQLite. To start directly on PostgreSQL or MySQL, set both fields in the configuration file. The DSN is a secret: configure `NVR_ENCRYPTION_KEY` so lalmax-nvr encrypts it when saving the configuration.

The database migration page in **Settings** can test and save a PostgreSQL or MySQL destination. The account needs permission to create tables and indexes and to read and write rows in the selected database. Create the database itself before connecting.

## Migrate from SQLite in the Web UI

1. Back up the SQLite file and confirm that the PostgreSQL or MySQL destination database is empty.
2. Open **Settings → Database migration**, select the destination driver, enter its DSN, test the connection, and save the target.
3. Choose **Migrate and switch** and confirm. Storage operations wait while lalmax-nvr initializes the destination, copies rows in batches, checks each table's row count, and calibrates PostgreSQL sequences.
4. On success, the running process switches to the destination connection and persists it as the active database. The original SQLite file is kept in place.

The migration API returns a per-table row count when the operation completes. Do not point the application at a database that contains unrelated or valuable tables: migration refuses destinations that are not empty or are not recognized as an interrupted migration created by this application.

If the process stops during copying, the target transaction rolls back its copied rows. The migration marker allows a retry against that same destination. If configuration saving fails after the copy, SQLite stays active; retrying recopies the latest source state. Database operations pause during the migration to prevent writes from being lost between copy and cutover.

## Current scope

The Web migration flow currently copies **SQLite to PostgreSQL or MySQL**. It does not migrate PostgreSQL to MySQL, change the recording-file layout, or delete the source SQLite file. Keep the source backup until the target has been checked in normal operation.
