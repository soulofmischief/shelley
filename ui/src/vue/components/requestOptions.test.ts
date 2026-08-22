import { normalizeRequestOptionsForModel } from "./requestOptions";

let passed = 0;
let failed = 0;

function expectOptions(
  selection: Parameters<typeof normalizeRequestOptionsForModel>[0],
  model: Parameters<typeof normalizeRequestOptionsForModel>[1],
  want: ReturnType<typeof normalizeRequestOptionsForModel>,
) {
  const got = normalizeRequestOptionsForModel(selection, model);
  if (got.pro === want.pro && got.fast === want.fast) {
    passed++;
  } else {
    failed++;
    console.error(`FAIL: got ${JSON.stringify(got)}, want ${JSON.stringify(want)}`);
  }
}

expectOptions(
  { pro: true, fast: true },
  { supports_pro_mode: true, supports_fast_mode: true },
  { pro: true, fast: true },
);
expectOptions({ pro: true, fast: true }, { supports_fast_mode: true }, { pro: false, fast: true });
expectOptions({ pro: true, fast: true }, { supports_pro_mode: true }, { pro: true, fast: false });
expectOptions({ pro: true, fast: true }, undefined, { pro: false, fast: false });
expectOptions(
  { pro: false, fast: false },
  { supports_pro_mode: true, supports_fast_mode: true },
  { pro: false, fast: false },
);

if (failed > 0) process.exit(1);
console.log(`requestOptions: ${passed} passed`);
