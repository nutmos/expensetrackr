---
name: github-pr
description: Use when changing things on this repo on the main branch.
---

# When to use

- Required: change on main branch
- Optional: change on non-main branch

# Instructions

When you would like to make change on a main branch, always create a new branch and raise a PR to merge to main branch. If it is non-main branches, this practice is optional.

The PR should have the following information.

- Header should be categorized into feat, fix, doc, or tool along with the package or component it intends to change. If the CVE is involved, it must indicate CVE number in the PR header.
- Header should come with the brief description.
- Body should have Summary, Change Type and Test Coverage
- Summary will write the summary of this PR with issue link that it relates or closes.
- Change type should choose only 1 type that is the most related. It must be 1 type to be chosen.
- Test should mention how the test has been done for this change

Example:
Header: feat(transaction): support transaction type

Body:
```
## Summary

This PR implements a transaction type feature.

Fixes #1234

## Change Type

- [x] Feature: New features that do not change the existing functionalities
- [ ] Change: Changes on the code that may affect the existing functionalities
- [ ] Bugfix: Fix bug and security issues
- [ ] Document: Update documents only
- [ ] Tools: Related to tools like CI/CD or Deployment Script

## Test

Unit Testing

```
