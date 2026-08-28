# Test Validity Review Policy v1alpha1

`test_validity`のreview観点とprovider instructionsの正本は、provider非依存の
[test_validity Agent Package instructions](../../../../agent-packages/test_validity/v1alpha1/instructions.md) へ
移した。本fileは既存仕様書とIssueからのhuman-readableな入口だけを提供し、Review Requestの
`policyRefs`やAgent Package digestの入力には使わない。

Package manifest、input/output schema、tool profile、fixtures、immutable request構築、structured output
validationの契約は[Agent Package Protocol](../contracts/agent-package-protocol-v1alpha1.md)を参照する。
Review Request/Result、freshness、failureとgateの契約は
[Implementation–Review Protocol](../contracts/review-protocol-v1alpha1.md)を参照する。

Codex Skill、Claude agent/plugin、provider固有promptをこのfileから別の正本として派生させない。必要な
launcherはrepositoryのAgent Packageをそのまま読み、Package refにbindされた同じinstructions/schemaを
使う。
