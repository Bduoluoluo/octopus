import argparse
import json
import sqlite3
from pathlib import Path


def audit(database):
    connection = sqlite3.connect(Path(database).resolve().as_uri() + '?mode=ro', uri=True)
    connection.row_factory = sqlite3.Row
    try:
        connection.execute('PRAGMA query_only = ON')
        connection.execute('BEGIN')
        channels = [dict(row) for row in connection.execute('SELECT id, name, type, enabled FROM channels ORDER BY id')]
        groups = [dict(row) for row in connection.execute('SELECT id, name FROM groups ORDER BY id')]
        items = [dict(row) for row in connection.execute('SELECT group_id, channel_id FROM group_items ORDER BY group_id, id')]
        key_channels = {row[0] for row in connection.execute("SELECT DISTINCT channel_id FROM channel_keys WHERE enabled = 1 AND channel_key <> ''")}
    finally:
        connection.close()
    retired = {channel['id'] for channel in channels if channel['type'] not in (0, 1, 2)}
    usable = {channel['id'] for channel in channels if channel['type'] in (0, 1, 2) and channel['enabled'] and channel['id'] in key_channels}
    affected = {item['group_id'] for item in items if item['channel_id'] in retired}
    available = {item['group_id'] for item in items if item['channel_id'] in usable}
    return {
        'read_only': True,
        'scope': 'Configured enabled channels and enabled nonempty keys; runtime health, quotas, and model filters are not evaluated.',
        'retired_channels': [channel for channel in channels if channel['id'] in retired],
        'groups_referencing_retired_channels': [group for group in groups if group['id'] in affected],
        'groups_without_configured_active_candidates': [group for group in groups if group['id'] not in available],
    }


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description='Read-only protocol retirement audit; never emits key material.')
    parser.add_argument('database', type=Path, help='Octopus SQLite database path')
    arguments = parser.parse_args()
    print(json.dumps(audit(arguments.database), ensure_ascii=False, indent=2))
