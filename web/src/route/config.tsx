import { lazyWithPreload } from './lazy-with-preload';
import { lazy, ComponentType } from 'react';
import type { LucideIcon } from 'lucide-react';
import {
    Activity,
    Home,
    Radio,
    Settings,
    Sparkles,
    Waypoints,
} from 'lucide-react';
import type { NavItem } from '@/components/modules/navbar/nav-store';
import { ADVANCED_NAV, PRIMARY_NAV } from '@/components/modules/navbar/nav-store';

export type LazyComponent = ReturnType<typeof lazy> & {
    preload: () => Promise<{ default: ComponentType<Record<string, never>> }>
};

export interface RouteConfig {
    id: NavItem;
    /** i18n key under navbar.* */
    labelKey: string;
    icon: LucideIcon;
    component: LazyComponent;
    /** primary | advanced — controls main nav vs overflow */
    tier: 'primary' | 'advanced';
}

const Home_Module = lazyWithPreload(() => import('@/components/modules/home').then(m => ({ default: m.Home })));
const Channel_Module = lazyWithPreload(() => import('@/components/modules/channel').then(m => ({ default: m.Channel })));
const Model_Module = lazyWithPreload(() => import('@/components/modules/model').then(m => ({ default: m.Model })));
const Group_Module = lazyWithPreload(() => import('@/components/modules/group').then(m => ({ default: m.Group })));
const Log_Module = lazyWithPreload(() => import('@/components/modules/log').then(m => ({ default: m.Log })));
const Setting_Module = lazyWithPreload(() => import('@/components/modules/setting').then(m => ({ default: m.Setting })));

const ROUTE_BY_ID: Record<Exclude<NavItem, 'site'>, Omit<RouteConfig, 'tier'>> = {
    home: { id: 'home', labelKey: 'home', icon: Home, component: Home_Module },
    group: { id: 'group', labelKey: 'route', icon: Waypoints, component: Group_Module },
    log: { id: 'log', labelKey: 'traffic', icon: Activity, component: Log_Module },
    setting: { id: 'setting', labelKey: 'setting', icon: Settings, component: Setting_Module },
    channel: { id: 'channel', labelKey: 'channel', icon: Radio, component: Channel_Module },
    model: { id: 'model', labelKey: 'model', icon: Sparkles, component: Model_Module },
};

/** Full route table (primary first). */
export const ROUTES: RouteConfig[] = [
    ...PRIMARY_NAV.map((id) => ({ ...ROUTE_BY_ID[id as Exclude<NavItem, 'site'>], tier: 'primary' as const })),
    ...ADVANCED_NAV.map((id) => ({ ...ROUTE_BY_ID[id as Exclude<NavItem, 'site'>], tier: 'advanced' as const })),
];

export const PRIMARY_ROUTES = ROUTES.filter((r) => r.tier === 'primary');
export const ADVANCED_ROUTES = ROUTES.filter((r) => r.tier === 'advanced');

export const CONTENT_MAP = ROUTES.reduce((acc, route) => {
    acc[route.id] = route.component;
    return acc;
}, {} as Record<string, LazyComponent>);
