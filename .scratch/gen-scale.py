#!/usr/bin/env python3
"""Generate a SYNTHETIC 650+ issue bd store with heterogeneous deps.

No fleet or real data: every title/description/label is generated from
synthetic word pools. Structure mimics a large engineering backlog:
multi-level parent chains (some past the 5-level cap), blocked-by edges,
mixed priorities, statuses, assignees, labels, and description lengths.
"""
import json
import random
import subprocess
import sys

random.seed(20260905)
STORE = ".scratch/scale"
PREFIX = "syn"

ADJ = ["quantum", "teal", "modular", "recursive", "silent", "amber", "copper",
       "linen", "cobalt", "glass", "paper", "static", "tidal", "nominal",
       "hollow", "civic", "lunar", "verbal", "solar", "flint"]
NOUN = ["router", "indexer", "harness", "gateway", "ledger", "beacon", "cache",
        "parser", "cluster", "adapter", "shard", "refinery", "pipeline",
        "scanner", "conveyor", "digest", "atlas", "relay", "vault", "loom"]
VERB = ["harden", "migrate", "refactor", "instrument", "document", "tune",
        "repair", "audit", "replace", "benchmark", "simplify", "sunset",
        "wrap", "parallelize", "backfill"]
LABELS = ["syn-infra", "syn-ui", "syn-docs", "syn-ops", "syn-research", "syn-sec"]
PEOPLE = ["ada@example.org", "grace@example.org", "linus@example.org",
          "hedy@example.org", "alan@example.org"]

def title():
    return f"{random.choice(VERB).capitalize()} the {random.choice(ADJ)} {random.choice(NOUN)}"

def description():
    kind = random.random()
    if kind < 0.2:
        return ""
    if kind < 0.6:
        return f"Synthetic fixture: {title().lower()} for scale validation. This row exists only to exercise rendering at fleet scale."
    return (f"Synthetic fixture for scale validation.\n\n"
            f"Background: the {random.choice(ADJ)} {random.choice(NOUN)} needs work "
            f"because {random.choice(['throughput regressed', 'the path is untested', 'docs drifted', 'capacity is tight'])}.\n\n"
            f"Steps:\n- measure the current {random.choice(NOUN)}\n- apply the {random.choice(ADJ)} change\n- re-measure and record results")

nodes = []
edges = []
key_of = {}
n = 0

def add_node(parent_key=None, **kw):
    global n
    n += 1
    key = f"n{n}"
    node = {"key": key, "title": title(), "priority": random.choice([0, 0, 1, 1, 1, 2, 2, 2, 3, 4])}
    if parent_key:
        node["parent_key"] = parent_key
    if random.random() < 0.6:
        node["labels"] = random.sample(LABELS, random.randint(1, 3))
    if random.random() < 0.5:
        node["description"] = description()
    if random.random() < 0.35:
        node["assignee"] = random.choice(PEOPLE)
    if random.random() < 0.3:
        node["type"] = random.choice(["task", "bug", "chore", "story"])
    nodes.append(node)
    key_of[key] = True
    return key

# 40 top-level chains, depth 1..7 (some past the 5-level cap), breadth 1..6
for chain in range(40):
    depth = random.choice([1, 2, 3, 3, 4, 5, 5, 6, 7])
    parent = None
    for level in range(depth):
        parent = add_node(parent)
    for _ in range(random.randint(0, 6)):
        add_node(parent)

# 180 leaf tasks under fresh mid-level roots
for _ in range(180):
    root = add_node()
    for _ in range(random.randint(0, 4)):
        add_node(root)

# flat standalone tasks to reach 650+
while n < 680:
    add_node()

# heterogeneous blocked-by / blocks edges between unrelated issues
keys = [node["key"] for node in nodes]
for _ in range(170):
    a, b = random.sample(keys, 2)
    if a != b:
        edges.append({"from_key": a, "to_key": b, "type": random.choice(["blocked-by", "blocks", "relates-to"])})

open("/tmp/scale-plan.json", "w").write(json.dumps({"nodes": nodes, "edges": edges}))
print(f"plan: {len(nodes)} nodes, {len(edges)} edges", file=sys.stderr)
