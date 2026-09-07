import { create } from 'zustand'
import { persist } from 'zustand/middleware'

/** All navigable page ids (including advanced). */
export type NavItem = 'home' | 'site' | 'channel' | 'group' | 'model' | 'log' | 'setting'

/** Primary IA (方案 A): 概览 / 接入 / 路由 / 流量 / 设置 */
export const PRIMARY_NAV: NavItem[] = ['home', 'group', 'log', 'setting']

/** Advanced pages collapsed under "更多" */
export const ADVANCED_NAV: NavItem[] = ['channel', 'model']

/** Full order for direction animation (primary first, then advanced). */
export const NAV_ORDER: NavItem[] = [...PRIMARY_NAV, ...ADVANCED_NAV]

interface NavState {
    activeItem: NavItem
    prevItem: NavItem | null
    direction: number
    setActiveItem: (item: NavItem) => void
}

export const useNavStore = create<NavState>()(
    persist(
        (set, get) => ({
            activeItem: 'home',
            prevItem: null,
            direction: 0,
            setActiveItem: (item) => {
                const { activeItem } = get()
                if (activeItem === item) return
                const currentIndex = NAV_ORDER.indexOf(activeItem)
                const newIndex = NAV_ORDER.indexOf(item)
                const direction = newIndex >= 0 && currentIndex >= 0
                    ? (newIndex > currentIndex ? 1 : -1)
                    : 1

                set({
                    activeItem: item,
                    prevItem: activeItem,
                    direction,
                })
            },
        }),
        {
            name: 'nav-storage',
            // Migrate unknown persisted values
            merge: (persisted, current) => {
                const p = (persisted || {}) as Partial<NavState>
                const active = p.activeItem
                const valid = active && NAV_ORDER.includes(active as NavItem)
                    ? (active as NavItem)
                    : current.activeItem
                return {
                    ...current,
                    ...p,
                    activeItem: valid,
                }
            },
        },
    ),
)
