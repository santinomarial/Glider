# Documentation standard

This standard keeps Glider's documentation navigable, testable, and useful
during design reviews and incidents. It applies to every Markdown file under
`docs/`.

## Information architecture

Write each page for one primary reader need:

| Type | Reader question | Required shape | Glider location |
|---|---|---|---|
| Tutorial | “Can you teach me?” | Guided learning path with a safe result | Add `docs/tutorials/` when the first tutorial is written |
| How-to | “How do I achieve this outcome?” | Preconditions, ordered actions, verification, rollback | `docs/operations/` |
| Reference | “What exactly is the contract?” | Precise inputs, outputs, invariants, limits, and errors | `docs/design/` |
| Explanation | “Why is it designed this way?” | Context, trade-offs, alternatives, consequences | `docs/architecture/` and `docs/adr/` |

Do not mix a tutorial, runbook, and design specification in one page. Link
between them instead. This separation follows the
[Diátaxis documentation model](https://diataxis.fr/).

## Required page structure

Use the smallest structure that satisfies the page's job. A substantial page
should normally contain:

1. One level-one title that names the subject.
2. A two-to-four sentence purpose and scope statement.
3. Preconditions or assumptions when actions depend on environment state.
4. The main content under descriptive level-two headings.
5. Verification criteria for procedures and operational claims.
6. Failure behavior or rollback where an action can leave partial state.
7. Related documents using relative repository links.

Do not number headings manually. Stable descriptive anchors survive inserted
sections better than `## 4.2` anchors.

## Writing rules

- Lead with the invariant, decision, or operator outcome.
- Name the owning component. Avoid “the system” when `gliderd`, the scheduler,
  or etcd is the actual actor.
- Distinguish desired state, observed state, and durable state.
- Use RFC 2119-style terms (`MUST`, `SHOULD`, `MAY`) only for normative
  contracts, and define the enforcement point.
- Pair every timeout, retry, capacity, or security claim with its configured
  value or authoritative link.
- Mark examples as examples. Never make fixture output look like production
  evidence.
- Prefer tables for exact comparisons and prose for reasoning. Avoid tables
  containing long narrative paragraphs.
- Use fenced code blocks with a language identifier. Commands must be
  copyable; placeholders use `<angle-brackets>`.
- Use relative links for repository content so links work on branches and in
  clones, as recommended by
  [GitHub's Markdown guidance](https://docs.github.com/en/get-started/writing-on-github/getting-started-with-writing-and-formatting-on-github/basic-writing-and-formatting-syntax#relative-links).

## Architecture diagrams

Use diagrams only when they clarify boundaries, relationships, or sequence.
Every diagram must include nearby prose that states:

- its question and audience;
- its scope and excluded concerns;
- the meaning of arrow direction and line style;
- protocols on cross-process edges;
- trust or failure boundaries where relevant;
- the source-of-truth owner for durable state.

Use the C4 zoom vocabulary—system context, container, component, and code—but
only add levels that answer a real question. The
[C4 model guidance](https://c4model.com/diagrams) notes that context and
container views are sufficient for many teams. Glider adds dynamic and
deployment views because reconciliation and failure domains are central to
its safety model.

Embed conservative Mermaid syntax in fenced `mermaid` blocks because GitHub
renders it natively. Keep node identifiers stable, quote labels containing
punctuation, and avoid experimental diagram types. GitHub documents the
supported format in
[Creating diagrams](https://docs.github.com/en/get-started/writing-on-github/working-with-advanced-formatting/creating-diagrams).

## Review checklist

- [ ] The page has one clear reader outcome and one level-one heading.
- [ ] Claims match current code, configuration, and release status.
- [ ] Internal links resolve relative to the file.
- [ ] Commands identify required privilege and destructive effects.
- [ ] Procedures include success checks and rollback or failure handling.
- [ ] Diagrams have a declared scope, legend, and labeled cross-process edges.
- [ ] Mermaid parses using the repository documentation validation command.
- [ ] No secret, private hostname, local absolute path, or fabricated evidence
      appears in examples.
