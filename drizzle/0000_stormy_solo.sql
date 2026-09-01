CREATE TABLE `installation_events` (
	`event_id` text PRIMARY KEY NOT NULL,
	`installed_at` text DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')) NOT NULL,
	`version` text NOT NULL,
	`os` text NOT NULL,
	`arch` text NOT NULL,
	`source` text NOT NULL,
	`installer` text NOT NULL
);
--> statement-breakpoint
CREATE INDEX `installation_events_installed_at_idx` ON `installation_events` (`installed_at`);