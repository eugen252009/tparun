# How to publish your work with TPA.run

This workflow is for humans and coding agents publishing a Debian package built by any project. The project builds the `.deb`; TPA.run handles repository discovery and publication. Do not add project-specific HTTP upload, authentication, or APT repository-generation code.

Authentication must already be configured. Obtain the intended repository name, URL, or bootstrap reference from the project or operator before proceeding.

## Workflow

1. Discover repositories you own:

   ```sh
   tparun list
   ```

2. Match the intended repository against the supplied name, URL, or bootstrap reference. Confirm the exact canonical repository URL. **Never publish to a guessed or merely similar repository.** If the reference does not identify one unambiguous repository, stop and ask.

3. Identify and stage the exact built artifact:

   ```sh
   tparun stage <package.deb>
   ```

4. Inspect the staged workspace:

   ```sh
   tparun status
   ```

5. Before publishing, verify all of the following:
   - The target repository URL is the intended repository.
   - Local package identity, version, and architecture match expectations. Inspect with `dpkg-deb -f <package.deb> Package Version Architecture`.
   - The local artifact SHA-256 from `sha256sum <package.deb>` matches the staged SHA-256 shown by `tparun status`.
   - The staged filename, package name, version, and architecture match the exact artifact you built.
   - The target repository does not already contain this released package identity/version. If it does, stop; do not replace or republish it.

   Abort if any identity, version, hash, or repository check fails. Do not continue on a mismatch or uncertainty.

6. Only after verification, publish to the confirmed canonical URL:

   ```sh
   tparun publish <repository-url>
   ```

   Review any publication plan or confirmation shown by the CLI. Do not approve a replacement or otherwise unexpected result.

7. Inspect the final publication result:

   ```sh
   tparun status
   ```

   Confirm the publication completed successfully. If it is pending or failed, report that state accurately; do not report it as published.

The CLI transparently splits manifests larger than the server's 64-file request limit into sequential batches. These are stored in one temporary publication session; no batch is independently queued or activated. After all batches are accepted, finalization creates one durable publication job. The complete candidate is still validated and activated atomically. The client waits for each batch acknowledgement before sending the next, retaining the normal admission, capacity, ownership, and per-request file-count checks. Failed uploads preserve local staging and abort the temporary session where possible; incomplete sessions are discarded on server restart. `--no-wait` queues the single finalized job and preserves local staging. Final verification still validates the resulting repository; session-upload hashes are not a substitute for it.

8. Report the repository, package filename, version, SHA-256, and final publication status.

## Example

For `example_1.2.0~beta1_amd64.deb`, calculate its local SHA-256 and inspect its package fields, then run `tparun list`, match the operator-provided repository, `tparun stage example_1.2.0~beta1_amd64.deb`, and `tparun status`. Compare the staged identity and SHA-256 with the local artifact and confirm the version is not already released. Only then run `tparun publish <confirmed-repository-url>` and `tparun status` again.

## Safety rules

- Never publish to a guessed repository or before inspecting staged state.
- Never overwrite an existing released version.
- Never bypass the TPA.run CLI with direct HTTP or `curl` publication when the CLI is available.
- Building a package is not publishing it. Publishing a package is not runtime activation or deployment.
- Do not claim runtime activation merely because publication succeeded.
- Abort on any mismatch in repository, package identity, SHA-256, or version.
