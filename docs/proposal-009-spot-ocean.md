# Spot ocean integration

## Authors

Spot By NetApp Ocean (@spotinst/sig-developers)

## Status

In process.

## Table of Contents
<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
    - [Goals](#goals)
    - [Non-Goals](#non-goals)
    - [Linked Docs](#linked-docs)
- [Proposal](#proposal)
- [Design Details](#design-details)
    - [Test Plan](#test-plan)
- [Alternatives](#alternatives)
<!-- /toc -->

## Summary

We implemented Spot Ocean structures that utilize the eksctl Cluster and NodeGroup structures with release `0.144.0`. This implementation
allows spot-ocean users to use eksctl in various ways on their clusters and node groups,
there are no dependencies with the eksctl structures that could bring issues in the future.

The value in integrating Spot Ocean with `eksctl` is simply to give a vast amount of AWS customers a way of:

a) Creating new clusters and/or node groups with spot ocean integration within the same
single command.

b) Modifying and deleting clusters and/or node groups with spot ocean integration within the same
single command.

## Motivation

The overall motivation of this proposal is to solve 2 problem:

- There are many AWS customers that have eks clusters that demand a spot ocean integration.
- AWS Customers want to integrate their eks clusters and nodegroups with spot ocean via eksctl's configuration.

### Goals

- AWS users can create spot ocean clusters and nodegroups using eksctl.
- AWS users can modify their spot ocean cluster configs and node groups using eksctl.
- AWS users can perform utility actions on their ocean clusters and nodegroups using eksctl.

### Non-Goals

- The integration is solely meant for spot ocean customer, it is not come to replace eks managed node groups in any sort of shape or form.

### Linked Docs

[Original PR]().
[Current eksctl docs](../userdocs/src/usage/spot).
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

## Design Details

The new arg option `--spot-ocean` will be added to `eksctl create cluster` and `eksctl create nodegroup`. That option will also be supported in the ClusterConfig file for both managed and self-managed nodegroups.

- For more details feel free to browse our [spot-ocean guides](../userdocs/src/usage/spot/ocean/spot-ocean-cluster.md)

### Test Plan

With each new feature and maintenance that was made, we check the following:

- Running all the existing unit tests to make sure nothing broke from our changes.
- Creation of new eks clusters on various k8s versions, from 1.23-1.27 currently.
- Creation and modifications of nodegroups inside those clusters.
- Utility actions regarding the ocean management part within eksctl.

## Alternatives

Alternatively, our clients use our own fork created eksctl [repo](https://github.com/spotinst/weaveworks-eksctl/releases/tag/v0.143.0) for their uses
