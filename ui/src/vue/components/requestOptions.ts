export interface RequestOptionModelCapabilities {
  supports_pro_mode?: boolean;
  supports_fast_mode?: boolean;
}

export interface RequestOptionsSelection {
  pro: boolean;
  fast: boolean;
}

export function normalizeRequestOptionsForModel(
  selection: RequestOptionsSelection,
  model: RequestOptionModelCapabilities | undefined,
): RequestOptionsSelection {
  return {
    pro: selection.pro && model?.supports_pro_mode === true,
    fast: selection.fast && model?.supports_fast_mode === true,
  };
}
