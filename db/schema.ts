import { sql } from "drizzle-orm";
import { index, sqliteTable, text } from "drizzle-orm/sqlite-core";

export const installationEvents = sqliteTable(
  "installation_events",
  {
    eventId: text("event_id").primaryKey(),
    installedAt: text("installed_at")
      .notNull()
      .default(sql`(strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))`),
    version: text("version").notNull(),
    os: text("os").notNull(),
    arch: text("arch").notNull(),
    source: text("source").notNull(),
    installer: text("installer").notNull(),
  },
  (table) => [index("installation_events_installed_at_idx").on(table.installedAt)],
);
