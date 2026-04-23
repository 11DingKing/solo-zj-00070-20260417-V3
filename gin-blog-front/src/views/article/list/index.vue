<script setup>
import { onMounted, ref } from 'vue'
import { useRoute } from 'vue-router'
import { debouncedWatch } from '@vueuse/core'
import dayjs from 'dayjs'

import BannerPage from '@/components/BannerPage.vue'
import { convertImgUrl } from '@/utils'
import api from '@/api'

const route = useRoute()

const loading = ref(true)
const articleList = ref([])
const name = ref(route.query.name)
const keyword = ref('')
const searchLoading = ref(false)

async function fetchArticles(searchKeyword = '') {
  searchLoading.value = true
  try {
    const resp = await api.getArticles({
      category_id: route.params.categoryId,
      tag_id: route.params.tagId,
      keyword: searchKeyword || undefined,
    })
    articleList.value = resp.data
  }
  finally {
    searchLoading.value = false
    loading.value = false
  }
}

debouncedWatch(
  keyword,
  (newKeyword) => {
    fetchArticles(newKeyword)
  },
  { debounce: 300 },
)

onMounted(() => {
  fetchArticles()
})

function clearSearch() {
  keyword.value = ''
}
</script>

<template>
  <BannerPage :loading="loading" :title="`${route.meta?.title} - ${name}`" label="article_list">
    <div class="mb-6">
      <div class="relative mx-auto max-w-2xl">
        <div class="pointer-events-none absolute inset-y-0 left-0 flex items-center pl-4">
          <div class="i-mdi:magnify text-xl text-gray-400" />
        </div>
        <input
          v-model="keyword"
          type="text"
          class="block w-full rounded-full border-0 bg-white py-3 pl-12 pr-12 text-gray-900 outline-none ring-1 ring-gray-200 ring-inset placeholder:text-gray-400 focus:ring-2 focus:ring-violet-300 transition-300 shadow-sm hover:shadow-md"
          placeholder="输入关键词搜索文章标题或摘要..."
        />
        <button
          v-if="keyword"
          class="absolute inset-y-0 right-0 flex items-center pr-4 text-gray-400 hover:text-gray-600 transition-300"
          @click="clearSearch"
        >
          <div class="i-mdi:close-circle text-xl" />
        </button>
        <div
          v-if="searchLoading"
          class="absolute inset-y-0 right-0 flex items-center pr-4"
        >
          <div class="i-mdi:loading text-xl text-violet-500 animate-spin" />
        </div>
      </div>
    </div>

    <div v-if="articleList.length" class="grid grid-cols-12 gap-4">
      <div
        v-for="article of articleList"
        :key="article.id"
        class="col-span-12 lg:col-span-4 md:col-span-6"
      >
        <div class="animate-zoom-in animate-duration-650 rounded-xl bg-white pb-2 shadow-md transition-300 hover:shadow-2xl">
          <div class="overflow-hidden">
            <RouterLink :to="`/article/${article.id}`">
              <img
                :src="convertImgUrl(article.img)"
                class="h-[220px] w-full rounded-t-xl transition-600 hover:scale-110"
              />
            </RouterLink>
          </div>
          <div>
            <div class="space-y-1.5">
              <RouterLink :to="`/article/${article.id}`">
                <p class="inline-block px-3 pt-2 hover:color-violet">
                  {{ article.title }}
                </p>
              </RouterLink>
              <div class="flex justify-between px-3">
                <span class="flex items-center">
                  <span class="i-mdi:clock-outline mr-1" />
                  <span> {{ dayjs(article.created_at).format('YYYY-MM-DD') }} </span>
                </span>
                <RouterLink
                  :to="`/categories/${article.category_id}?name=${article.category?.name}`"
                >
                  <div class="flex items-center text-#4c4948 transition-300 hover:color-violet">
                    <span class="i-ic:outline-bookmark mr-1" />
                    <span> {{ article.category?.name }} </span>
                  </div>
                </RouterLink>
              </div>
            </div>
            <div class="my-2 h-0.5 bg-gray-200" />
            <div class="px-3 space-x-1.5">
              <RouterLink
                v-for="tag of article.tags"
                :key="tag.id"
                :to="`/tags/${tag.id}?name=${tag.name}`"
              >
                <span class="inline-block cursor-pointer rounded-xl from-green-400 to-blue-500 bg-gradient-to-r px-2 py-1 text-xs text-white transition-500 hover:scale-110 hover:from-pink-500 hover:to-yellow-500">
                  {{ tag.name }}
                </span>
              </RouterLink>
            </div>
          </div>
        </div>
      </div>
    </div>

    <div v-else-if="keyword && !loading" class="f-c-c py-16">
      <div class="text-center">
        <div class="i-mdi:file-search-outline text-6xl text-gray-300 mb-4" />
        <p class="text-lg text-gray-500">
          未找到包含 "<span class="text-violet-500 font-semibold">{{ keyword }}</span>" 的文章
        </p>
        <p class="text-sm text-gray-400 mt-2">请尝试其他关键词</p>
      </div>
    </div>

    <div v-else-if="!loading" class="f-c-c py-16">
      <div class="text-center">
        <div class="i-mdi:file-document-outline text-6xl text-gray-300 mb-4" />
        <p class="text-lg text-gray-500">暂无文章</p>
      </div>
    </div>
  </BannerPage>
</template>
