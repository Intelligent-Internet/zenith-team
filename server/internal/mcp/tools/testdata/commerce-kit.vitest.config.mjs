import { fileURLToPath } from "node:url";

export default {
  root: fileURLToPath(new URL(".", import.meta.url)),
  // Templates and their tests must not inherit a developer's parent tsconfig.
  esbuild: {
    tsconfigRaw: JSON.stringify({
      compilerOptions: {
        target: "ES2022",
        useDefineForClassFields: true,
      },
    }),
  },
  test: {
    include: ["boardly-commerce-kit.test.ts"],
  },
};
