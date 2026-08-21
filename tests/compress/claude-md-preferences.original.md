# User Preferences

## Code Style

I strongly prefer TypeScript with strict mode enabled for all new code. Please don't use `any` type unless there's genuinely no way around it, and if you do, leave a comment explaining the reasoning. I find that taking the time to properly type things catches a lot of bugs before they ever make it to runtime.

When writing React components, I always want to use functional components with hooks. I have no interest in class components — they're harder to read and test in my experience. For state management, I prefer keeping state as local as possible and only lifting it up when truly necessary. I'd rather have a component re-render a bit more than have a complex global state tree.

I like to keep my imports organized with a clear separation between external packages, internal modules, and relative imports. Please use path aliases (like `@/components/...`) instead of deeply nested relative paths. It makes refactoring much easier and the code more readable.

## Testing Approach

Please always write tests for any new functionality. I prefer writing tests that describe behavior from the user's perspective rather than testing implementation details. For example, test that "clicking the submit button creates a new task" rather than "the handleSubmit function calls the createTask service."

For React components, use Testing Library and avoid directly testing internal state or lifecycle methods. For API endpoints, write integration tests that hit the real database — we've had too many incidents where mocked tests passed but production broke.

I don't need 100% code coverage, but I do want meaningful coverage on critical paths. Business logic in services should have thorough unit tests. UI components should have tests for the happy path and key error states.

## Communication Style

I'm a senior engineer who has been working on this project for about two years. You don't need to explain basic programming concepts to me. I appreciate concise, direct communication that gets to the point quickly. If there's a tradeoff to make, just lay out the options with pros and cons rather than writing paragraphs of explanation.

When suggesting code changes, please show me the actual code rather than describing what to change in words. I can read code faster than I can read a paragraph describing code. If you're making changes across multiple files, show them all at once rather than one at a time.

Don't add comments to obvious code. Comments should explain "why" not "what." If the code needs a comment to explain what it does, it probably needs to be refactored instead.

## Workflow Preferences

Before making any changes, please read the existing code first to understand the patterns already in use. I'd rather you follow the existing conventions even if they're not your preferred approach — consistency matters more than individual preference in a team codebase.

When making pull requests, keep them focused and small. I'd rather review three small PRs than one large one. Each PR should ideally do one thing and do it well. If you find something else that needs fixing while working on a feature, create a separate PR for it.

Run the linter and type checker before committing. The pre-commit hooks should catch most issues, but it's good practice to run them manually too, especially after a large refactoring session.

## Things to Avoid

Please do not add console.log statements for debugging — use the structured logging utility at `src/lib/logger.ts` instead. Console logs have a bad habit of making it to production and cluttering the output.

Don't install new dependencies without discussing it first. I want to keep the bundle size manageable and avoid situations where we have three libraries that do the same thing. If there's a built-in Node.js or browser API that can do the job, prefer that over adding a package.

Avoid creating abstraction layers that only have one consumer. Premature abstraction is worse than duplication in my experience. Wait until you have at least three places that need the same thing before extracting a shared utility.

Never commit `.env` files or any file containing secrets, API keys, or credentials. The `.env.example` file should have placeholder values that indicate what each variable is for without revealing actual secrets.
