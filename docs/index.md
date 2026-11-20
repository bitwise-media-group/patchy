---
title: Patchy
template: home.html
hide:
  - navigation
  - toc
---

<!--
  The home page is rendered entirely by overrides/home.html (the landing).
  This body only provides the page title and meta description for the <head>.
-->

An end-to-end pipeline for triaging and fixing security findings, using Kubernetes custom resources as the state
machine. Alerts accumulate into `Finding` resources, each shown as a GitHub tracking issue. A sandboxed coding agent
investigates each one, and high-confidence fixes become pull requests for human review.
