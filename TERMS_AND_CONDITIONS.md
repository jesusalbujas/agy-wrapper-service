# Terms and Conditions

**Last updated: November 2, 2026**

## 1. Community purpose

`agy-wrapper-service` is shared as a community project: from the community, for the community. It is developed and distributed on a non-profit basis to make a generic HTTP interface for the Antigravity CLI available to people who choose to run it.

The project itself does not promise hosted access or support. A person or organization operating an instance is responsible for that instance and for telling its users how it is configured.

## 2. Fees and third-party costs

No fee is charged by this project for the source code. This does not mean that running it is cost-free. Hosting, network traffic, storage, the Antigravity CLI, model access, and other third-party services may have separate costs or limits. Those costs and terms belong to the relevant operator or provider and are not covered by this project.

## 3. Acceptable use

By using an instance, you agree to:

- Use it only with accounts, workspaces, files, and systems you are authorized to access.
- Follow the terms and policies of the CLI, model, and other services you use through it.
- Avoid unlawful activity, abuse, attempts to bypass access controls, and requests intended to harm people or systems.
- Review generated output and any proposed actions before relying on them.

The operator may restrict or suspend access to an instance to protect its users, systems, or provider accounts.

## 4. Third-party services

This project is independent and is not affiliated with or endorsed by Google or the providers of models made available through the Antigravity CLI. Availability, supported models, account requirements, quotas, and data handling may change under the applicable provider's terms. Users and operators must review those terms themselves.

## 5. Prompts, files, and credentials

Prompts and file references submitted to the wrapper are passed to the Antigravity CLI for processing. The CLI or model provider may handle that information under its own policies. Do not submit confidential, personal, regulated, or otherwise sensitive information unless you are authorized to do so and have confirmed that the deployment and provider are suitable for it.

For the current implementation:

- Uploaded files are written to the configured `UPLOAD_DIR` (default: `/app/uploads`). Operators should arrange retention and deletion; uploaded files may remain there after a request.
- The API log records a shortened excerpt of prompt or instruction text and, for file requests, the uploaded file name.
- The Docker Compose configuration mounts the operator's Antigravity session directory into the container. Operators must protect that directory and its credentials.

Operators should tell users what data their instance retains and remove uploaded files, logs, and credentials when no longer needed.

## 6. Security and operator responsibilities

The wrapper currently has no built-in request authentication and invokes Antigravity with automatic tool approval enabled. Anyone who can reach an unprotected instance may be able to submit prompts that use the CLI's available tools and configured workspace. **Do not expose the API directly to the public internet.**

The operator is responsible for restricting network access, adding appropriate authentication and transport protection, limiting mounted files and workspace access, protecting credentials, setting resource limits, and monitoring the instance. Users must use only instances they are permitted to access.

## 7. Availability and output

The software is provided as-is for community use. No uptime, response time, support, or compatibility commitment is made. Generated output can be incomplete or incorrect. A qualified person should verify it before using it to make decisions or perform actions, especially actions that affect data, accounts, infrastructure, or other people.

## 8. Changes and acceptance

These terms may be updated alongside the project. The version included with the instance describes the project usage expectations at that time. By using an instance, you acknowledge these expectations and the separate terms that apply to the CLI and other providers.

This document describes project usage expectations; it is not a representation that a particular deployment complies with every legal or regulatory requirement. Operators remain responsible for assessing the requirements that apply to their deployment.

## 9. Questions

For project questions, use the repository's issue tracker if it is enabled. For questions about a specific instance, contact that instance's operator.
