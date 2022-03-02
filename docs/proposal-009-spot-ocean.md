# Spot ocean integration

## Authors

Spot By NetApp Ocean (@spotinst/sig-developers)

## Status

in process.

## Table of Contents
<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
    - [Goals](#goals)
    - [Non-Goals](#non-goals)
    - [Linked Docs](#linked-docs)
- [Proposal](#proposal)
- [Design Details](#design-details)
- [Alternatives](#alternatives)
<!-- /toc -->

## Summary

We implemented Spot Ocean structures integration on the cluster and nodeGroup structures with release `0.144.0`. This implementation
allows spot-ocean users to use eksctl in various ways on their clusters and node groups.

The value in integrating Spot Ocean with `eksctl` is simply to give users a way of:

a) setting up their new clusters and/or node groups with spot ocean immediately after it is created, as part of the same
single command.

b) using spot ocean utilities that are unique to spot ocean users.

## Motivation

The overall motivation of this proposal is to solve 1 problem:

- Users want to integrate their clusters with spot ocean via eksctl's configuration.

### Goals

- Users can create spot ocean clusters using eksctl.
- Users can modify their spot ocean node groups using eksctl.
- Users can perform a certain amount of utility actions on their ocean clusters and nodegroups using eksctl.

### Non-Goals

- the integration is solely meant for spot ocean customer, it is not come to replace eks managed node groups in any sort of shape or form.

### Linked Docs

[Original PR]().
[Current eksctl docs]().
[Current Flux api object in eksctl]().
[Expansion issue]().

## Proposal

This design proposes adding a new field `spotOcean` to both cluster and nodegroup level,
and creates cluster with spot ocean managed nodegroups.

for example:

```bash
eksctl create cluster \
 --name example \
 --spot-ocean
 --managed=false
```

will result in a new spot ocean cluster.

### Risks and Mitigations

<!--
What are the risks of this proposal, and how do we mitigate?
What could get in the way of this solution being implemented the way we want?
(This is technical stuff: do not count natural disasters or pandemics.)
Think broadly.  For example, consider how this will impact or be impacted by other
things within the project as well as other components/APIs it interacts with.
-->

## Design Details

as can be seeing in the Proposal section, there's a new option called `--spot-ocean` will be added to `eksctl create cluster` and `eksctl create nodegroup`. These options will also be supported in the ClusterConfig file for both managed and self-managed nodegroups.
- for more details feel free to browse our [spot-ocean guides](../userdocs/src/usage/spot/ocean/spot-ocean-cluster.md)

## Alternatives

Alternatively, our clients use our own fork created eksctl [repo](https://github.com/spotinst/weaveworks-eksctl/releases/tag/v0.143.0) for their uses
