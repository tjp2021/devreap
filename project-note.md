---
type: project-note
title: "Devreap"
status: "active"
priority: "Medium"
business: "Internal"
repo: "devreap"
current_shape: "standalone-app"
target_shape: "standalone-app"
core_capability: "orphaned developer-process detection and cleanup with multi-signal scoring"
surfaces:
  - cli
  - daemon
updated: 2026-04-10
created: 2026-04-10
tags:
  - project
  - go
  - devtools
  - process-management
---

# Devreap

## Overview

Developer tool for detecting and cleaning up orphaned local processes such as leaked MCP servers, dev servers, browsers, and ffmpeg jobs using multi-signal scoring instead of naive kill rules.

## Current Status

`~/YNG/os/_shared/devreap` is a live local Go project with a working CLI/daemon model, LaunchAgent install path, and a clear operator-facing use case. It looks more like a focused product/tool than a capability-extraction target.

## Architecture

- Current shape: `standalone-app`
- Target shape: `standalone-app`
- Core capability: `orphaned developer-process detection and cleanup with multi-signal scoring`
- Interface surfaces: `cli`, `daemon`
- Main operating modes: scan, daemon start/stop, install/uninstall, doctor, logs, kill, patterns

## Extraction Assessment

Leave this repo as-is unless a concrete cross-project reuse case appears. The pattern matcher, scoring model, and daemon behavior are coherent inside the product and do not currently justify a capability-first split.

## Tasks

- [ ] Keep the CLI and daemon workflow trustworthy on this machine
- [ ] Keep safety rules and scoring logic conservative enough to avoid bad kills
- [ ] Only extract internals if another project genuinely needs them
