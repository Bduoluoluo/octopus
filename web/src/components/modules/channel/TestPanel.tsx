import { useMemo, useState } from 'react';
import { Play, ChevronDown } from 'lucide-react';
import { useTranslations } from 'next-intl';
import {
    useTestChannelModel,
    type Channel,
    type CreateChannelRequest,
    type TestModelResult,
    ChannelType,
} from '@/api/endpoints/channel';
import type { ChannelFormData } from './Form';
import { Badge } from '@/components/ui/badge';
import { cn } from '@/lib/utils';
import { toast } from '@/components/common/Toast';

type TestStatus = 'idle' | 'testing' | 'ok' | 'client_error' | 'server_error' | 'network_error';

interface ModelTestState {
    status: TestStatus;
    delay_ms?: number;
    status_code?: number;
    error?: string;
}

interface TestPanelProps {
    /**
     * 宸蹭繚瀛樻笭閬撴椂浼犲畬鏁?Channel,id 瀛楁蹇呭～銆?
     * 鏂板缓/缂栬緫鍦烘櫙浼?formData(杩樻湭淇濆瓨)銆?
     * 浜岄€変竴;涓や釜閮戒紶鏃朵紭鍏堢敤 channel銆?
     */
    channel?: Channel;
    formData?: ChannelFormData;
}

/*
 * 娓犻亾缂栬緫寮圭獥鍙充晶鐨?妯″瀷娴嬭瘯"绐勬爮銆?
 * 鍗曡灞曠ず姣忎釜宸查€夋ā鍨?閫氳繃鐘舵€佺偣 + HTTP 鐘舵€佺爜 chip 鍙嶉鏈€杩戞祴璇曠粨鏋溿€?
 * 鏀寔鍗曠嫭娴嬭瘯鏌愯 / 鍕鹃€夊悗鎵归噺娴嬭瘯(閫愪釜涓茶)銆?
 *
 * 鐘舵€佺偣棰滆壊:
 *   200 -> emerald;  4xx -> red;  5xx -> amber;
 *   0 (缃戠粶閿?瓒呮椂) -> red;  鏈祴 -> gray;  娴嬭瘯涓?-> amber pulse銆?
 */
export function TestPanel({ channel, formData }: TestPanelProps) {
    const t = useTranslations('channel.test');
    const testModel = useTestChannelModel();

    // 杈撳叆鍙傛暟(浠呭綋鍓嶅脊绐楀唴鏈夋晥,鍏抽棴鍗充涪)
    const [timeoutSec, setTimeoutSec] = useState<number>(30);
    const [keyIndex, setKeyIndex] = useState<number>(0);
    const [selected, setSelected] = useState<Set<string>>(new Set());
    const [results, setResults] = useState<Map<string, ModelTestState>>(new Map());
    const [isBatch, setIsBatch] = useState(false);

    // 妯″瀷闆嗗悎涓庣粺璁?model + custom_model 鍚堝苟鍘婚噸
    const modelStr = channel ? channel.model : (formData?.model ?? '');
    const customModelStr = channel ? channel.custom_model : (formData?.custom_model ?? '');
    const modelInfo = useMemo(() => {
        const split = (s?: string) =>
            (s ?? '').split(',').map((x) => x.trim()).filter(Boolean);
        const fromSync = split(modelStr);
        const fromCustom = split(customModelStr);
        const all = Array.from(new Set([...fromSync, ...fromCustom]));
        return { syncCount: fromSync.length, customCount: fromCustom.length, all };
    }, [modelStr, customModelStr]);

    // 褰撳墠绫诲瀷鐨?keys 鏁扮粍;鏂板缓鍦烘櫙涓嬬敤 formData.keys(鍙兘娌℃湁 id)
    const formKeys = formData?.keys;
    const channelKeys = channel?.keys;
    const keys = useMemo(() => {
        if (channelKeys) return channelKeys;
        return (formKeys ?? []) as Array<{
            id?: number;
            channel_key: string;
            remark?: string;
            enabled?: boolean;
        }>;
    }, [channelKeys, formKeys]);

    const channelType = channel?.type ?? formData?.type;
    const isEmbedding = channelType === ChannelType.OpenAIEmbedding;

    // 娌℃湁 keys 鏃朵笉鑳芥祴
    // 閫夊畾鐨?key 涓嬫爣鍚堟硶鎬?
    const effectiveKeyIndex = Math.min(Math.max(0, keyIndex), Math.max(0, keys.length - 1));
    const hasValidKey = Boolean(keys[effectiveKeyIndex]?.channel_key.trim());
    const hasBaseURL = (channel?.base_urls ?? formData?.base_urls ?? []).some((base) => base.url.trim());
    const isBusy = isBatch || testModel.isPending;
    const selectedModels = modelInfo.all.filter((model) => selected.has(model));

    const buildRequest = (model: string) => {
        const base = {
            model,
            key_index: effectiveKeyIndex,
            timeout_ms: timeoutSec * 1000,
        };
        if (channel) {
            return { ...base, channel_id: channel.id };
        }
        // 鏂板缓/缂栬緫鍦烘櫙:鎶?formData 杞垚 create 璇锋眰椋庢牸 (note:杩欐槸鏈夋剰浼犳槑鏂?- 瑙佸悗绔敞閲?
        if (formData) {
            const tempChannel: CreateChannelRequest = {
                name: formData.name,
                type: formData.type,
                enabled: formData.enabled,
                base_urls: (formData.base_urls ?? []).filter((u) => u.url.trim()).map((u) => ({
                    url: u.url.trim(),
                    delay: Number(u.delay || 0),
                })),
                keys: (formData.keys ?? []).map((k) => ({
                    enabled: k.enabled ?? true,
                    channel_key: k.channel_key.trim(),
                    remark: k.remark ?? '',
                })),
                model: formData.model,
                custom_model: formData.custom_model,
                proxy_mode: formData.proxy_mode,
                proxy_config_id: formData.proxy_mode === 'pool' ? formData.proxy_config_id : null,
                auto_sync: formData.auto_sync ?? false,
                auto_group: formData.auto_group,
                custom_header: formData.custom_header ?? [],
                param_override: formData.param_override ?? null,
                match_regex: formData.match_regex ?? null,
            };
            return { ...base, channel: tempChannel };
        }
        return base;
    };

    const updateResult = (model: string, next: ModelTestState) => {
        setResults((prev) => {
            const m = new Map(prev);
            m.set(model, next);
            return m;
        });
    };

    const toState = (r: TestModelResult): ModelTestState => {
        let status: TestStatus;
        if (r.status_code >= 200 && r.status_code < 300 && !r.error) status = 'ok';
        else if (r.status_code === 0) status = 'network_error';
        else if (r.status_code >= 500) status = 'server_error';
        else if (r.status_code >= 400) status = 'client_error';
        else status = 'client_error'; // 2xx 涓潪 200 / 3xx 閲嶅畾鍚?瑙嗕负瀹㈡埛绔敊
        return { status, delay_ms: r.delay_ms, status_code: r.status_code, error: r.error };
    };

    // 娴嬭瘯澶辫触鎵嶆彁绀?鎴愬姛淇濇寔闈欓粯,鐢辫鍐呯姸鎬佺偣鍙嶉銆?
    // 鐩存帴灞曠ず鍚庣杩斿洖鐨?error 鍘熸枃,涓嶅啀浜屾鍔犲伐鏍煎紡銆?
    const notifyFailure = (model: string, state: ModelTestState) => {
        const detail = state.error?.trim();
        if (detail) {
            toast.error(detail, { description: model });
        }
    };

    const runOne = async (model: string) => {
        const req = buildRequest(model);
        updateResult(model, { status: 'testing' });
        try {
            const r = await testModel.mutateAsync(req as Parameters<typeof testModel.mutateAsync>[0]);
            const next = toState(r);
            updateResult(model, next);
            if (next.status !== 'ok') {
                notifyFailure(model, next);
            }
        } catch (e) {
            // HTTP 灞傞敊璇?鍚庣 4xx/5xx 绛夋姤閿?璧拌繖閲?
            const next: ModelTestState = {
                status: 'client_error',
                error: (e as { message?: string })?.message ?? 'request failed',
            };
            updateResult(model, next);
            notifyFailure(model, next);
        }
    };

    const runBatch = async () => {
        if (isBusy || selectedModels.length === 0) return;
        setIsBatch(true);
        try {
            // 鍚庣涓茶娴?鍓嶇閫愪釜鍙?杩欐牱 UI 杩涘害鍙
            for (const model of selectedModels) {
                await runOne(model);
            }
        } finally {
            setIsBatch(false);
        }
    };

    const toggleSelect = (model: string) => {
        setSelected((prev) => {
            const next = new Set(prev);
            if (next.has(model)) next.delete(model);
            else next.add(model);
            return next;
        });
    };

    const toggleSelectAll = () => {
        setSelected((prev) => (prev.size === modelInfo.all.length ? new Set() : new Set(modelInfo.all)));
    };

    // 娓叉煋杈呭姪
    const renderDot = (state?: ModelTestState) => {
        if (!state || state.status === 'idle') {
            return <span className="inline-block size-2 rounded-full bg-gray-300" />;
        }
        if (state.status === 'testing') {
            return <span className="inline-block size-2 rounded-full bg-amber-500 animate-pulse" />;
        }
        if (state.status === 'ok') {
            return <span className="inline-block size-2 rounded-full bg-emerald-500" />;
        }
        if (state.status === 'server_error') {
            return <span className="inline-block size-2 rounded-full bg-amber-500" />;
        }
        // client_error / network_error
        return <span className="inline-block size-2 rounded-full bg-red-500" />;
    };

    const renderStatusChip = (state?: ModelTestState) => {
        if (!state || state.status === 'idle' || state.status === 'testing') return null;
        if (state.status === 'network_error') {
            return (
                <Badge variant="secondary" className="h-5 px-1.5 text-[10px] font-mono bg-red-500/15 text-red-700 dark:text-red-400">
                    {t('statusNetwork')}
                </Badge>
            );
        }
        const cls = state.status === 'ok'
            ? 'bg-emerald-500/15 text-emerald-700 dark:text-emerald-400'
            : state.status === 'server_error'
                ? 'bg-amber-500/15 text-amber-700 dark:text-amber-400'
                : 'bg-red-500/15 text-red-700 dark:text-red-400';
        return (
            <Badge variant="secondary" className={cn('h-5 px-1.5 text-[10px] font-mono', cls)} title={state.error}>
                {state.status_code}
            </Badge>
        );
    };

    const renderDelay = (state?: ModelTestState) => {
        if (!state || state.status === 'idle') return <span className="text-[10px] text-muted-foreground">{t('statusUntested')}</span>;
        if (state.status === 'testing') return <span className="text-[10px] text-muted-foreground">...</span>;
        if (typeof state.delay_ms !== 'number') return null;
        return (
            <span className="text-[10px] font-mono text-muted-foreground">
                {state.delay_ms < 1000 ? `${state.delay_ms}ms` : `${(state.delay_ms / 1000).toFixed(1)}s`}
            </span>
        );
    };

    return (
        <aside className="w-full min-w-0 rounded-xl border border-border bg-card/50 p-4 flex flex-col gap-3">
            {/* 鏍囬 + 瓒呮椂杈撳叆 */}
            <header className="flex flex-wrap items-end justify-between gap-2 pb-2 border-b border-border">
                <h3 className="text-sm font-semibold text-card-foreground">{t('title')}</h3>
                <label className="flex items-center gap-1 text-xs text-muted-foreground">
                    {t('timeoutLabel')}
                    <input
                        type="number"
                        min={1}
                        max={300}
                        value={timeoutSec}
                        onChange={(e) => setTimeoutSec(Math.max(1, Math.min(300, Number(e.target.value) || 30)))}
                        className="w-12 px-1 py-0.5 text-xs bg-background border border-border rounded-md text-right"
                        disabled={isEmbedding || isBusy}
                    />
                    <span>s</span>
                </label>
            </header>
            <p className="text-xs text-muted-foreground">{t(isEmbedding ? 'unsupported' : 'hint')}</p>

            {/* 妯″瀷鏁伴噺缁熻 */}
            <div className="text-xs text-muted-foreground">
                {t('modelCount', { total: modelInfo.all.length, sync: modelInfo.syncCount, custom: modelInfo.customCount })}
            </div>

            {/* Key 閫夋嫨涓嬫媺 */}
            <div className="text-xs space-y-1">
                <div className="text-muted-foreground">{t('keyLabel')}</div>
                <div className="relative">
                    <select
                        value={effectiveKeyIndex}
                        onChange={(e) => setKeyIndex(Number(e.target.value))}
                        disabled={isEmbedding || isBusy || keys.length === 0}
                        aria-label={t('keyLabel')}
                        className="w-full appearance-none bg-background border border-border rounded-md px-2 py-1 text-xs font-mono pr-6 truncate"
                    >
                        {keys.length === 0 && <option value={0}>{t('noKeys')}</option>}
                        {keys.map((k, idx) => {
                            const label = k.remark?.trim()
                                ? truncate(k.remark.trim(), 10)
                                : `Key #${idx + 1}`;
                            return <option key={idx} value={idx}>{label || `#${idx}`}</option>;
                        })}
                    </select>
                    <ChevronDown className="absolute right-1.5 top-1/2 -translate-y-1/2 size-3 pointer-events-none text-muted-foreground" />
                </div>
            </div>

            {/* 妯″瀷鍒楄〃 */}
            <div className="space-y-1 max-h-72 overflow-y-auto">
                {modelInfo.all.length === 0 ? (
                    <div className="text-xs text-muted-foreground text-center py-6">{t('noModels')}</div>
                ) : (
                    modelInfo.all.map((m) => {
                        const state = results.get(m);
                        const checked = selected.has(m);
                        return (
                            <div
                                key={m}
                                className="flex items-center gap-1.5 px-2 py-1 rounded-md bg-background/50 border border-border/50 hover:bg-background transition-colors group"
                            >
                                <input
                                    type="checkbox"
                                    checked={checked}
                                    onChange={() => toggleSelect(m)}
                                    disabled={isEmbedding || isBusy}
                                    aria-label={m}
                                    className="size-3.5 accent-primary cursor-pointer shrink-0"
                                />
                                <span
                                    className="flex-1 min-w-0 truncate text-xs font-mono text-card-foreground"
                                    title={state?.error || m}
                                >
                                    {m}
                                </span>
                                <button
                                    type="button"
                                    onClick={() => runOne(m)}
                                    disabled={isEmbedding || !hasValidKey || !hasBaseURL || isBusy}
                                    className="p-1 rounded-md hover:bg-accent disabled:opacity-40 disabled:cursor-not-allowed shrink-0 transition-colors"
                                    title={t('testButtonTitle')}
                                >
                                    <Play className="size-3 text-foreground" />
                                </button>
                                {renderDot(state)}
                                {renderDelay(state)}
                                {renderStatusChip(state)}
                            </div>
                        );
                    })
                )}
            </div>

            {/* 搴曢儴鎵归噺鎿嶄綔 */}
            <footer className="flex flex-wrap items-center justify-between gap-2 pt-2 border-t border-border text-xs">
                <span className="text-muted-foreground">
                    {t('selectedCount', { selected: selectedModels.length, total: modelInfo.all.length })}
                </span>
                <div className="flex items-center gap-1">
                    <button
                        type="button"
                        onClick={toggleSelectAll}
                        disabled={isEmbedding || isBusy || modelInfo.all.length === 0}
                        className="px-2 py-1 rounded-md border border-border bg-background hover:bg-accent disabled:opacity-40 transition-colors"
                    >
                        {selected.size === modelInfo.all.length ? t('deselectAll') : t('selectAll')}
                    </button>
                    <button
                        type="button"
                        onClick={runBatch}
                        disabled={isEmbedding || !hasValidKey || !hasBaseURL || isBusy || selectedModels.length === 0}
                        className="px-2 py-1 rounded-md bg-primary text-primary-foreground hover:bg-primary/90 disabled:opacity-40 transition-colors"
                    >
                        {isBatch ? t('testing') : t('batchTest')}
                    </button>
                </div>
            </footer>
        </aside>
    );
}

function truncate(s: string, n: number): string {
    if (s.length <= n) return s;
    return s.slice(0, n) + '...';
}
