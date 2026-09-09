#!/usr/bin/env python3
"""Emit the synthetic hierarchy fixture plan (bd create --graph format)."""
import json
print(json.dumps({
    "nodes": [
        {"key": "root", "title": "Root task alpha", "priority": 0, "labels": ["epic", "infra"]},
        {"key": "a1", "title": "Child A1 of alpha", "priority": 1, "parent_key": "root"},
        {"key": "gc", "title": "Great grandchild with many labels", "priority": 2, "parent_key": "a1",
         "labels": ["epic", "infra", "ui", "docs", "ops"],
         "description": "Has many labels."},
        {"key": "l3", "title": "Level 3 node", "priority": 2, "parent_key": "gc", "assignee": "ada@example.org"},
        {"key": "l4", "title": "Level 4 node", "priority": 2, "parent_key": "l3", "labels": ["sec"]},
        {"key": "l5", "title": "Level 5 node", "priority": 2, "parent_key": "l4"},
        {"key": "l6", "title": "Level 6 node", "priority": 3, "parent_key": "l5"},
        {"key": "a2", "title": "Child A2 (closed)", "priority": 3, "parent_key": "root"},
        {"key": "a3", "title": "Grandchild A1a", "priority": 1, "parent_key": "root",
         "labels": ["research"], "description": "Blocked leaf."},
        {"key": "beta", "title": "Root task beta", "priority": 2, "labels": ["docs", "epic"]},
        {"key": "solo", "title": "Standalone tagged task", "priority": 1,
         "labels": ["frontend", "ui", "a11y", "perf", "research", "synthetic"],
         "description": "Has many labels."},
    ],
    "edges": [
        {"from_key": "l5", "to_key": "a3", "type": "blocked-by"},
        {"from_key": "root", "to_key": "a3", "type": "blocks"},
    ],
}))
