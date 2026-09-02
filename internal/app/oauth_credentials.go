package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/BeeAPI-AI/beeapi/internal/beeapi"
	"github.com/BeeAPI-AI/beeapi/internal/state"
)

type oauthKeyChoice struct {
	Key     beeapi.OAuthAPIKey
	Models  []string
	Options []beeapi.ModelOption
	Err     error
}

func (r *runner) authorizeWithExistingOAuth(existing state.OAuthAccount, endpoint string, noOpen bool, mode string, agents ...string) (authorizationResult, error) {
	selectedIssuer, err := beeapi.OAuthIssuerForEntrance(endpoint)
	if err != nil {
		return authorizationResult{}, err
	}
	if existing.Issuer != selectedIssuer {
		r.line(r.out, "  当前入口属于另一 OAuth 安全域，正在重新授权。", "  The current endpoint belongs to another OAuth security domain; authorizing again.")
		return r.authorizeAndSelectOAuthCredentials(endpoint, noOpen, mode, agents...)
	}
	identity := strings.TrimSpace(existing.Username)
	if identity == "" {
		identity = strings.TrimSpace(existing.Email)
	}
	if identity == "" {
		identity = r.text("已连接账户", "connected account")
	}
	r.format(r.out, "  检测到 BeeAPI OAuth 账户：%s\n", "  BeeAPI OAuth account detected: %s\n", identity)
	r.line(r.out, "  1. 继续使用此账户（推荐）", "  1. Continue with this account (recommended)")
	r.line(r.out, "  2. 在网页重新授权", "  2. Authorize again in the browser")
	r.line(r.out, "  3. 改用粘贴 API Key", "  3. Paste an API Key instead")
	choice, err := r.askLocalized("  请选择 [1]: ", "  Select [1]: ")
	if err != nil && !errors.Is(err, io.EOF) {
		return authorizationResult{}, err
	}
	switch strings.TrimSpace(choice) {
	case "", "1":
		client, account, sessionErr := r.oauthAccountClient(r.ctx)
		if sessionErr == nil && containsScope(account.Scope, "api_keys:export") {
			result, selectErr := r.selectAndExportOAuthCredentials(client, account, endpoint, mode, agents...)
			if selectErr == nil || !oauthInteractiveAuthorizationRequired(selectErr) {
				return result, selectErr
			}
			r.line(r.out, "  现有连接需要重新确认一次性 Key 领取权限。", "  The existing connection needs renewed approval for one-time API Key export.")
		} else if sessionErr != nil {
			r.format(r.errOut, "  现有 OAuth 连接无法继续：%v\n", "  The existing OAuth connection cannot continue: %v\n", sessionErr)
			r.line(r.out, "  正在重新打开 BeeAPI 网页授权。", "  Opening BeeAPI authorization again.")
		} else {
			r.line(r.out, "  持续登录令牌不保留 Key 导出权限，需要在网页重新确认。", "  Persistent sign-in does not retain API Key export permission; browser approval is required again.")
		}
		return r.authorizeAndSelectOAuthCredentials(endpoint, noOpen, mode, agents...)
	case "2":
		return r.authorizeAndSelectOAuthCredentials(endpoint, noOpen, mode, agents...)
	case "3":
		credentials, pasteErr := r.pasteAPIKey(endpoint)
		if pasteErr == nil {
			r.clearOAuthAccountForCompatibility()
		}
		return authorizationResult{Credentials: credentials}, pasteErr
	default:
		return authorizationResult{}, errors.New(r.text("登录方式只能选择 1、2 或 3", "Login method must be 1, 2, or 3"))
	}
}

func (r *runner) authorizeAndSelectOAuthCredentials(endpoint string, noOpen bool, mode string, agents ...string) (authorizationResult, error) {
	client, _, account, err := r.authorizeOAuthAccount(endpoint, noOpen)
	if err != nil {
		return authorizationResult{}, err
	}
	return r.selectAndExportOAuthCredentials(client, account, endpoint, mode, agents...)
}

func oauthInteractiveAuthorizationRequired(err error) bool {
	var apiErr *beeapi.APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	return apiErr.Reason == "oauth.step_up_required" || apiErr.Reason == "oauth.insufficient_scope" || apiErr.Reason == "oauth.invalid_token"
}

func oauthCapabilityUnavailable(err error) bool {
	if errors.Is(err, beeapi.ErrOAuthDiscoveryUnavailable) {
		return true
	}
	var apiErr *beeapi.APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	switch apiErr.Status {
	case http.StatusNotFound, http.StatusMethodNotAllowed, http.StatusNotImplemented:
		return true
	default:
		return false
	}
}

func (r *runner) selectAndExportOAuthCredentials(client *beeapi.Client, account state.OAuthAccount, endpoint, mode string, agents ...string) (authorizationResult, error) {
	if client == nil {
		return authorizationResult{}, errors.New(r.text("OAuth 账户客户端未初始化", "The OAuth account client is not initialized"))
	}
	ctx, cancel := context.WithTimeout(r.ctx, 15*time.Second)
	profile, err := client.OAuthAccount(ctx)
	cancel()
	if err != nil {
		return authorizationResult{}, fmt.Errorf(r.text("读取 BeeAPI 账户信息: %w", "Read BeeAPI account information: %w"), err)
	}
	account, err = r.updateOAuthAccountIdentity(account, profile.StableSubject(), profile.Username, profile.Email)
	if err != nil {
		return authorizationResult{}, err
	}

	ctx, cancel = context.WithTimeout(r.ctx, 15*time.Second)
	balance, balanceErr := client.OAuthAccountBalance(ctx)
	cancel()
	identity := strings.TrimSpace(profile.Username)
	if identity == "" {
		identity = strings.TrimSpace(profile.Email)
	}
	if identity == "" {
		identity = r.text("当前账户", "Current account")
	}
	if balanceErr == nil {
		r.format(r.out, "  ✓ 已登录 %s · 账户余额 %s\n", "  ✓ Signed in as %s · Account balance %s\n", identity, formatBalance(balance.Current(), balance.CurrencyCode()))
	} else {
		r.format(r.out, "  ✓ 已登录 %s · 余额暂时无法读取\n", "  ✓ Signed in as %s · Balance is temporarily unavailable\n", identity)
	}

	ctx, cancel = context.WithTimeout(r.ctx, 15*time.Second)
	keys, err := client.OAuthAPIKeys(ctx)
	cancel()
	if err != nil {
		return authorizationResult{}, fmt.Errorf(r.text("读取 BeeAPI API Key 列表: %w", "Read BeeAPI API Key list: %w"), err)
	}
	if len(keys) == 0 {
		return authorizationResult{}, errors.New(r.text("账户中没有 API Key，请先在 BeeAPI 创建一个", "This account has no API Keys; create one in BeeAPI first"))
	}

	choices := r.loadOAuthKeyChoices(client, keys)
	var selected []oauthKeyChoice
	if len(agents) == 1 && strings.TrimSpace(agents[0]) != "" {
		selected, err = r.selectOAuthKeyChoiceForAgent(choices, agents[0])
	} else {
		selected, err = r.selectOAuthKeyChoices(choices)
	}
	if err != nil {
		return authorizationResult{}, err
	}
	ids := make([]string, 0, len(selected))
	selectedByID := make(map[string]oauthKeyChoice, len(selected))
	for _, choice := range selected {
		id := strings.TrimSpace(string(choice.Key.ID))
		ids = append(ids, id)
		selectedByID[id] = choice
	}

	idempotencyKey, err := randomBase64URL(24)
	if err != nil {
		return authorizationResult{}, err
	}
	exported, err := r.createOAuthAPIKeyExport(client, ids, idempotencyKey)
	if err != nil {
		return authorizationResult{}, fmt.Errorf(r.text("一次性领取所选 API Key: %w", "Export the selected API Keys once: %w"), err)
	}
	credentials := make([]credentialMaterial, 0, len(exported.Credentials))
	seenCredentialIDs := make(map[string]bool, len(exported.Credentials))
	accountedSourceIDs := make(map[string]bool, len(selected))
	for _, item := range exported.Credentials {
		sourceID := strings.TrimSpace(string(item.APIKeyID))
		choice, ok := selectedByID[sourceID]
		if !ok {
			return authorizationResult{}, errors.New(r.text("BeeAPI 返回了未选择的 API Key", "BeeAPI returned an API Key that was not selected"))
		}
		if accountedSourceIDs[sourceID] {
			return authorizationResult{}, errors.New(r.text("BeeAPI 导出结果重复返回了同一 API Key", "The BeeAPI export returned the same API Key more than once"))
		}
		accountedSourceIDs[sourceID] = true
		name := strings.TrimSpace(item.KeyName)
		if name == "" {
			name = strings.TrimSpace(choice.Key.Name)
		}
		if name == "" {
			name = "BeeAPI API Key"
		}
		prefix := strings.TrimSpace(item.KeyPrefix)
		if prefix == "" {
			prefix = strings.TrimSpace(choice.Key.KeyPrefix)
		}
		if prefix == "" {
			prefix = safeKeyPrefix(item.APIKey)
		}
		credentialID := stableCredentialID(item.APIKey)
		if seenCredentialIDs[credentialID] {
			return authorizationResult{}, errors.New(r.text("BeeAPI 导出结果包含重复的 API Key", "The BeeAPI export contains a duplicate API Key"))
		}
		seenCredentialIDs[credentialID] = true
		credentials = append(credentials, credentialMaterial{
			ID: credentialID, Name: name, Prefix: prefix,
			Secret: item.APIKey, Models: append([]string(nil), choice.Models...),
			ModelOptions: append([]beeapi.ModelOption(nil), choice.Options...), ModelOptionsAuthoritative: true,
		})
	}
	for _, skipped := range exported.Skipped {
		sourceID := strings.TrimSpace(string(skipped.APIKeyID))
		if _, ok := selectedByID[sourceID]; !ok {
			return authorizationResult{}, errors.New(r.text("BeeAPI 跳过了一个未选择的 API Key", "BeeAPI skipped an API Key that was not selected"))
		}
		if accountedSourceIDs[sourceID] {
			return authorizationResult{}, errors.New(r.text("BeeAPI 导出结果重复返回了同一 API Key", "The BeeAPI export returned the same API Key more than once"))
		}
		accountedSourceIDs[sourceID] = true
		name := strings.TrimSpace(skipped.KeyName)
		if name == "" {
			name = r.text("未命名 API Key", "Unnamed API Key")
		}
		r.format(r.out, "    ↷ %s · 未领取：%s\n", "    ↷ %s · Not exported: %s\n", name, r.credentialSkipReason(skipped.Reason))
	}
	for sourceID := range selectedByID {
		if !accountedSourceIDs[sourceID] {
			return authorizationResult{}, errors.New(r.text("BeeAPI 导出结果遗漏了已选择的 API Key", "The BeeAPI export omitted a selected API Key"))
		}
	}
	if len(credentials) == 0 {
		return authorizationResult{}, errors.New(r.text("所选 API Key 均无法导出，请选择其他 Key", "None of the selected API Keys could be exported; choose different Keys"))
	}

	stored, err := r.checkpointCredentialMaterialsWithExport(mode, endpoint, credentials, exported.ExportID, exported.RetryUntil)
	if err != nil {
		return authorizationResult{}, err
	}
	if err := r.ackOAuthExport(client, exported.ExportID); err != nil {
		r.updatePendingSetup(mode, endpoint, stored, err)
		return authorizationResult{}, err
	}
	if err := r.clearPendingOAuthExport(mode, exported.ExportID); err != nil {
		return authorizationResult{}, err
	}
	r.format(r.out, "  ✓ 已安全保存并完成 %d 个所选 API Key 的导出收尾。\n", "  ✓ Securely stored and finalized export of %d selected API Key(s).\n", len(credentials))
	return authorizationResult{Credentials: credentials, Stored: stored}, nil
}

func (r *runner) createOAuthAPIKeyExport(client *beeapi.Client, ids []string, idempotencyKey string) (beeapi.OAuthAPIKeyExport, error) {
	baseCtx := r.ctx
	if baseCtx == nil {
		baseCtx = context.Background()
	}
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		ctx, cancel := context.WithTimeout(baseCtx, 20*time.Second)
		exported, err := client.CreateOAuthAPIKeyExport(ctx, ids, idempotencyKey)
		cancel()
		if err == nil {
			return exported, nil
		}
		lastErr = err
		if attempt == 2 || baseCtx.Err() != nil || !oauthRequestRetryable(err) {
			break
		}
		if attempt == 0 {
			r.line(r.out, "  Key 导出响应暂时中断，正在使用同一请求安全恢复…", "  The API Key export response was interrupted; safely recovering the same request…")
		}
		if err := waitOAuthRetry(baseCtx, time.Duration(attempt+1)*250*time.Millisecond); err != nil {
			return beeapi.OAuthAPIKeyExport{}, err
		}
	}
	return beeapi.OAuthAPIKeyExport{}, lastErr
}

func (r *runner) loadOAuthKeyChoices(client *beeapi.Client, keys []beeapi.OAuthAPIKey) []oauthKeyChoice {
	choices := make([]oauthKeyChoice, len(keys))
	var wait sync.WaitGroup
	for index := range keys {
		choices[index].Key = keys[index]
		if !oauthKeySelectableMetadata(keys[index]) {
			continue
		}
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			ctx, cancel := context.WithTimeout(r.ctx, 15*time.Second)
			defer cancel()
			options, err := client.OAuthAPIKeyModelOptions(ctx, string(keys[index].ID))
			if err != nil {
				choices[index].Err = err
				return
			}
			options = normalizeModelOptions(options)
			models := make([]string, 0, len(options))
			for _, option := range options {
				models = append(models, option.ID)
			}
			if len(models) == 0 {
				choices[index].Err = errors.New(r.text("当前没有可用模型", "No models are currently available"))
				return
			}
			choices[index].Options = options
			choices[index].Models = models
		}(index)
	}
	wait.Wait()
	return choices
}

func normalizeModelOptions(options []beeapi.ModelOption) []beeapi.ModelOption {
	seen := make(map[string]bool, len(options))
	result := make([]beeapi.ModelOption, 0, len(options))
	for _, option := range options {
		option.ID = strings.TrimSpace(option.ID)
		if option.ID == "" || seen[option.ID] {
			continue
		}
		seen[option.ID] = true
		result = append(result, option)
	}
	return result
}

func oauthKeySelectableMetadata(key beeapi.OAuthAPIKey) bool {
	status := strings.ToLower(strings.TrimSpace(key.Status))
	return key.Exportable && (status == "" || status == "enabled" || status == "active") && strings.TrimSpace(string(key.ID)) != ""
}

func (r *runner) selectOAuthKeyChoices(choices []oauthKeyChoice) ([]oauthKeyChoice, error) {
	r.line(r.out, "\n  选择要用于本机配置的 BeeAPI API Key", "\n  Choose BeeAPI API Keys for this computer")
	selectable := make([]int, 0, len(choices))
	for index, choice := range choices {
		name := strings.TrimSpace(choice.Key.Name)
		if name == "" {
			name = r.text("未命名 API Key", "Unnamed API Key")
		}
		prefix := strings.TrimSpace(choice.Key.KeyPrefix)
		if prefix == "" {
			prefix = r.text("前缀未知", "Unknown prefix")
		}
		if !oauthKeySelectableMetadata(choice.Key) {
			reason := strings.TrimSpace(choice.Key.UnavailableReason)
			if reason == "" {
				reason = strings.TrimSpace(choice.Key.Status)
			}
			fmt.Fprintf(r.out, "    × %s · %s · %s\n", name, prefix, r.credentialSkipReason(reason))
			continue
		}
		if choice.Err != nil {
			r.format(r.out, "    × %s · %s · 无法读取模型：%v\n", "    × %s · %s · Could not load models: %v\n", name, prefix, choice.Err)
			continue
		}
		selectable = append(selectable, index)
		fmt.Fprintf(r.out, "    %d. ✓ %s · %s · %d %s\n", len(selectable), name, prefix, len(choice.Models), r.text("个模型", "model(s)"))
	}
	if len(selectable) == 0 {
		return nil, errors.New(r.text("没有同时可导出且有可用模型的 API Key", "No API Key is both exportable and backed by available models"))
	}
	for {
		answer, err := r.askLocalized("  输入编号（可逗号多选，回车=1）: ", "  Enter number(s), comma-separated (Enter=1): ")
		if err != nil && !errors.Is(err, io.EOF) {
			return nil, err
		}
		answer = strings.TrimSpace(answer)
		if answer == "" {
			answer = "1"
		}
		seen := map[int]bool{}
		selected := make([]oauthKeyChoice, 0, len(selectable))
		valid := true
		for _, part := range strings.Split(answer, ",") {
			number, convErr := strconv.Atoi(strings.TrimSpace(part))
			if convErr != nil || number < 1 || number > len(selectable) {
				valid = false
				break
			}
			if !seen[number] {
				seen[number] = true
				selected = append(selected, choices[selectable[number-1]])
			}
		}
		if !valid || len(selected) == 0 {
			r.line(r.errOut, "  API Key 编号无效，请重新选择。", "  Invalid API Key number; choose again.")
			continue
		}
		if len(selected) > 10 {
			r.line(r.errOut, "  一次最多选择 10 个 API Key，请重新选择。", "  Select at most 10 API Keys at a time; choose again.")
			continue
		}
		return selected, nil
	}
}

func (r *runner) selectOAuthKeyChoiceForAgent(choices []oauthKeyChoice, agent string) ([]oauthKeyChoice, error) {
	r.line(r.out, "\n  选择要用于本机配置的 BeeAPI API Key", "\n  Choose the BeeAPI API Key for this configuration")
	selectable := make([]int, 0, len(choices))
	for index, choice := range choices {
		name := strings.TrimSpace(choice.Key.Name)
		if name == "" {
			name = r.text("未命名 API Key", "Unnamed API Key")
		}
		prefix := strings.TrimSpace(choice.Key.KeyPrefix)
		if prefix == "" {
			prefix = r.text("前缀未知", "Unknown prefix")
		}
		if !oauthKeySelectableMetadata(choice.Key) {
			reason := strings.TrimSpace(choice.Key.UnavailableReason)
			if reason == "" {
				reason = strings.TrimSpace(choice.Key.Status)
			}
			fmt.Fprintf(r.out, "    × %s · %s · %s\n", name, prefix, r.credentialSkipReason(reason))
			continue
		}
		if choice.Err != nil {
			r.format(r.out, "    × %s · %s · 无法读取模型：%v\n", "    × %s · %s · Could not load models: %v\n", name, prefix, choice.Err)
			continue
		}
		credential := credentialMaterial{
			Name: name, Models: choice.Models, ModelOptions: choice.Options, ModelOptionsAuthoritative: true,
		}
		compatible, compatibilityErr := compatibleModelsForAgent(agent, credential)
		if compatibilityErr != nil {
			r.format(r.out, "    × %s · %s · 不适用于 %s：%s\n", "    × %s · %s · Not compatible with %s: %s\n",
				name, prefix, agentLabel(agent), r.localizedErrorMessage(compatibilityErr))
			continue
		}
		selectable = append(selectable, index)
		fmt.Fprintf(r.out, "    %d. ✓ %s · %s · %d %s\n", len(selectable), name, prefix, len(compatible), r.text("个兼容模型", "compatible model(s)"))
	}
	if len(selectable) == 0 {
		return nil, fmt.Errorf(r.text("没有可用于 %s 的 API Key，请在 BeeAPI 检查 Key 路由与模型权限", "No API Key is compatible with %s; check its routing and model access in BeeAPI"), agentLabel(agent))
	}
	for {
		answer, err := r.askLocalized("  输入编号（回车=1）: ", "  Enter one number (Enter=1): ")
		if err != nil && !errors.Is(err, io.EOF) {
			return nil, err
		}
		answer = strings.TrimSpace(answer)
		if answer == "" {
			answer = "1"
		}
		number, convErr := strconv.Atoi(answer)
		if convErr != nil || number < 1 || number > len(selectable) {
			r.line(r.errOut, "  API Key 编号无效，请重新选择。", "  Invalid API Key number; choose again.")
			continue
		}
		return []oauthKeyChoice{choices[selectable[number-1]]}, nil
	}
}

func (r *runner) ackOAuthExport(client *beeapi.Client, exportID string) error {
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		ctx, cancel := context.WithTimeout(r.ctx, 12*time.Second)
		lastErr = client.AckOAuthAPIKeyExport(ctx, exportID)
		cancel()
		if lastErr == nil {
			return nil
		}
		var apiErr *beeapi.APIError
		if errors.As(lastErr, &apiErr) {
			switch apiErr.Reason {
			case "oauth.export_acknowledged":
				return nil
			case "oauth.export_unavailable":
				r.line(r.out, "  服务端导出窗口已结束；本地 Key 已安全保存，可继续配置。", "  The server export window ended; the locally stored Keys are safe and setup can continue.")
				return nil
			}
		}
		if attempt == 0 && r.ctx.Err() == nil {
			r.line(r.out, "  导出确认响应中断，正在安全重试…", "  Export acknowledgement was interrupted; retrying safely…")
		}
	}
	return fmt.Errorf(r.text("确认 API Key 已安全保存: %w", "Acknowledge that API Keys were stored safely: %w"), lastErr)
}

func (r *runner) resumePendingOAuthExport(pending state.PendingSetup) error {
	if strings.TrimSpace(pending.OAuthExportID) == "" {
		return nil
	}
	if !pending.OAuthExportRetryUntil.IsZero() && !time.Now().Before(pending.OAuthExportRetryUntil) {
		if err := r.clearPendingOAuthExport(pending.Mode, pending.OAuthExportID); err != nil {
			return err
		}
		r.line(r.out, "  上次导出窗口已结束；服务端密文已过期，本地保存的 Key 可继续使用。", "  The previous export window ended; the server ciphertext expired and the locally stored Keys remain usable.")
		return nil
	}
	client, account, err := r.oauthAccountClient(r.ctx)
	if err != nil {
		return r.finishPendingOAuthExportWithoutAck(pending, err)
	}
	if !containsScope(account.Scope, "api_keys:export") {
		return r.finishPendingOAuthExportWithoutAck(pending, errors.New("the persistent OAuth session does not retain API Key export permission"))
	}
	if err := r.ackOAuthExport(client, pending.OAuthExportID); err != nil {
		if oauthRequestRetryable(err) {
			return err
		}
		return r.finishPendingOAuthExportWithoutAck(pending, err)
	}
	if err := r.clearPendingOAuthExport(pending.Mode, pending.OAuthExportID); err != nil {
		return err
	}
	r.line(r.out, "  ✓ 已补充确认上次安全保存的 API Key。", "  ✓ Acknowledged the API Keys safely stored during the previous attempt.")
	return nil
}

func (r *runner) finishPendingOAuthExportWithoutAck(pending state.PendingSetup, cause error) error {
	if err := r.clearPendingOAuthExport(pending.Mode, pending.OAuthExportID); err != nil {
		return err
	}
	r.format(r.errOut, "  无法补发上次导出确认：%v\n", "  Could not resend the previous export acknowledgement: %v\n", cause)
	r.line(r.out, "  本地 Key 已安全保存；服务端短期密文会自动过期，继续后续配置。", "  The local Keys are safely stored. The short-lived server ciphertext will expire automatically; continuing setup.")
	return nil
}
